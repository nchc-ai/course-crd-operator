/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/nchc-ai/course-crd-operator/pkg/model"
	"github.com/nchc-ai/course-crd-operator/pkg/util"
	"github.com/nchc-ai/course-cron/pkg/cron"
	networkv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	coreinformers "k8s.io/client-go/informers/core/v1"
	networkv1informer "k8s.io/client-go/informers/networking/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	networkv1lister "k8s.io/client-go/listers/networking/v1"

	log "github.com/golang/glog"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
	apidb "github.com/nchc-ai/backend-api/pkg/model/db"
	coursev1alpha1 "github.com/nchc-ai/course-crd/pkg/apis/coursecontroller/v1alpha1"
	clientset "github.com/nchc-ai/course-crd/pkg/client/clientset/versioned"
	samplescheme "github.com/nchc-ai/course-crd/pkg/client/clientset/versioned/scheme"
	informers "github.com/nchc-ai/course-crd/pkg/client/informers/externalversions/coursecontroller/v1alpha1"
	listers "github.com/nchc-ai/course-crd/pkg/client/listers/coursecontroller/v1alpha1"

	"github.com/nitishm/go-rejson/v4"
)

// Controller is the controller implementation for Foo resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// sampleclientset is a clientset for our own API group
	sampleclientset clientset.Interface

	deploymentsLister appslisters.DeploymentLister
	deploymentsSynced cache.InformerSynced

	svcLister corelisters.ServiceLister
	svcSynced cache.InformerSynced

	pvcLister corelisters.PersistentVolumeClaimLister
	pvcSynced cache.InformerSynced

	courseLister listers.CourseLister
	courseSynced cache.InformerSynced

	ingressLister networkv1lister.IngressLister
	ingressSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder

	config               *model.Config
	clusterResourceLimit corev1.ResourceList
	hasBeenRewrite       map[string]bool
	db                   *gorm.DB
	redis                *rejson.Handler
}

// NewController returns a new sample controller
func NewController(
	kubeclientset kubernetes.Interface,
	sampleclientset clientset.Interface,
	deploymentInformer appsinformers.DeploymentInformer,
	svcInformer coreinformers.ServiceInformer,
	pvcInformer coreinformers.PersistentVolumeClaimInformer,
	ingressInformer networkv1informer.IngressInformer,
	courseInformer informers.CourseInformer,
	config *model.Config) *Controller {

	// Create event broadcaster
	// Add sample-controller types to the default Kubernetes Scheme so Events can be
	// logged for sample-controller types.
	utilruntime.Must(samplescheme.AddToScheme(scheme.Scheme))
	log.Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(log.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		kubeclientset:     kubeclientset,
		sampleclientset:   sampleclientset,
		deploymentsLister: deploymentInformer.Lister(),
		deploymentsSynced: deploymentInformer.Informer().HasSynced,
		svcLister:         svcInformer.Lister(),
		svcSynced:         svcInformer.Informer().HasSynced,
		courseLister:      courseInformer.Lister(),
		courseSynced:      courseInformer.Informer().HasSynced,
		pvcLister:         pvcInformer.Lister(),
		pvcSynced:         pvcInformer.Informer().HasSynced,
		ingressLister:     ingressInformer.Lister(),
		ingressSynced:     ingressInformer.Informer().HasSynced,
		workqueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Foos"),
		recorder:          recorder,
		config:            config,
		hasBeenRewrite:    make(map[string]bool),
	}

	log.Info("Setting up event handlers")
	// Set up an event handler for when Foo resources change
	courseInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueCourse,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueCourse(new)
		},
		//DeleteFunc: controller.removeIngressRule,
	})
	// Set up an event handler for when Deployment resources change. This
	// handler will lookup the owner of the given Deployment, and if it is
	// owned by a Foo resource will enqueue that Foo resource for
	// processing. This way, we don't need to implement custom logic for
	// handling Deployment resources. More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md
	deploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*appsv1.Deployment)
			oldDepl := old.(*appsv1.Deployment)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	svcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newSvc := new.(*corev1.Service)
			oldSvc := old.(*corev1.Service)
			if newSvc.ResourceVersion == oldSvc.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newIngress := new.(*networkv1.Ingress)
			oldIngress := old.(*networkv1.Ingress)
			if newIngress.ResourceVersion == oldIngress.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	return controller
}

func (c *Controller) Init() error {

	// initialize db client
	dbArgs := fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=utf8&parseTime=True",
		c.config.DBConfig.Username,
		c.config.DBConfig.Password,
		c.config.DBConfig.Host,
		c.config.DBConfig.Port,
		c.config.DBConfig.Database,
	)

	DB, err := gorm.Open("mysql", dbArgs)
	if err != nil {
		log.Fatalf("create database client fail: %s", err.Error())
		return err
	}
	c.db = DB

	addr := fmt.Sprintf("%s:%d", c.config.RedisConfig.Host, c.config.RedisConfig.Port)
	redis := rejson.NewReJSONHandler()
	cli := goredis.NewClient(&goredis.Options{Addr: addr})
	redis.SetGoRedisClient(cli)
	c.redis = redis

	// initialize resource limit
	err = c.detectResourceLimit(c.kubeclientset)
	if err != nil {
		log.Fatalf("detect cluster resource limit fail: %s", err.Error())
		return err
	}

	return nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	log.Info("Starting Course controller")

	// Wait for the caches to be synced before starting workers
	log.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.deploymentsSynced,
		c.svcSynced, c.courseSynced, c.pvcSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	log.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	log.Info("Started workers")
	<-stopCh
	log.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		log.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
func (c *Controller) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the Foo resource with this namespace/name
	course, err := c.courseLister.Courses(namespace).Get(name)
	if err != nil {
		// The Foo resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("course '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	// if current time is not in scheduled time (in cron format), delete the course object
	isMatch := false
	for _, cronExpression := range course.Spec.Schedule {
		if cron.IsNowMatchCronExpression(cronExpression, "Asia/Taipei") {
			isMatch = true
			break
		}
	}

	if !isMatch {
		log.Infof("Current time {%s} is not schedule {%s}, delete course {%s}",
			time.Now().String(), course.Spec.Schedule, course.ObjectMeta.Name)

		//delete db record
		audit := apidb.Audit{
			Model: apidb.Model{
				ID: name,
			},
		}

		c.db.Model(&audit).Update("deleted_by", "CRD")
		if err := c.db.Delete(audit).Error; err != nil {
			log.Warningf(fmt.Sprintf("Failed to mark job {%s} deletion audit information : %s", audit.ID, err.Error()))
		}

		job := apidb.Job{
			Model: apidb.Model{
				ID: name,
			},
		}

		if err := c.db.First(&job).Error; err != nil {
			log.Errorf("Failed to find job {%s} information : %s", job.ID, err.Error())
			return err
		}

		if err := c.db.Unscoped().Delete(job).Error; err != nil {
			log.Errorf("Failed to delete job {%s} information : %s", job.ID, err.Error())
			return err
		}

		// delete redis cache
		if _, err := c.redis.JSONDel(fmt.Sprintf("%s:%s", job.Provider, job.User), "."); err != nil {
			log.Errorf("Failed to delete job cache {%s:%s} information : %s", job.Provider, job.User, err.Error())
			return err
		}

		// delete Course CRD
		err := c.sampleclientset.NchcV1alpha1().Courses(course.Namespace).Delete(
			context.Background(), course.ObjectMeta.Name, metav1.DeleteOptions{},
		)
		if err != nil {
			log.Errorf("Delete course {%s} fail: %s", course.Name, err.Error())
			return err
		}
		return nil
	}

	// reconcile Service
	deploymentName := course.Name

	// Get the deployment with the name specified in Foo.spec
	deployment, err := c.deploymentsLister.Deployments(course.Namespace).Get(deploymentName)
	// If the resource doesn't exist, we'll create it
	if errors.IsNotFound(err) {
		deployment, err = c.kubeclientset.AppsV1().Deployments(course.Namespace).
			Create(
				context.Background(),
				newDeployment(course, c.clusterResourceLimit.DeepCopy()),
				metav1.CreateOptions{},
			)
	}

	// If an error occurs during Get/Create, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	if err != nil {
		log.Errorf("Get/Create Deployment {%s} fail: %s", deploymentName, err.Error())
		return err
	}

	// If the Deployment is not controlled by this Foo resource, we should log
	// a warning to the event recorder and ret
	if !metav1.IsControlledBy(deployment, course) {
		msg := fmt.Sprintf(MessageResourceExists, deployment.Name)
		c.recorder.Event(course, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf(msg)
	}

	svc, err := getOrCreateService(c, course)
	if err != nil {
		log.Errorf("Get/Create service of course {%s} fail: %s", course.Name, err.Error())
		return err
	}

	if !metav1.IsControlledBy(svc, course) {
		msg := fmt.Sprintf(MessageResourceExists, svc.Name)
		c.recorder.Event(course, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf(msg)
	}

	if course.Spec.AccessType == coursev1alpha1.AccessTypeIngress {
		ingress, err := getOrCreateIngress(c, course, svc.Name)
		if err != nil {
			log.Errorf("Get/Create Ingress of course {%s} fail: %s", course.Name, err.Error())
			return err
		}

		if !metav1.IsControlledBy(ingress, course) {
			msg := fmt.Sprintf(MessageResourceExists, svc.Name)
			c.recorder.Event(course, corev1.EventTypeWarning, ErrResourceExists, msg)
			return fmt.Errorf(msg)
		}

	}

	// create write volume if necessary
	if course.Spec.WritableVolume != nil {
		pvcName := fmt.Sprintf("%s", util.StringHashMD5(course.Spec.WritableVolume.Owner))
		_, err := c.pvcLister.PersistentVolumeClaims(course.Namespace).Get(pvcName)
		if errors.IsNotFound(err) {
			_, err = c.kubeclientset.CoreV1().PersistentVolumeClaims(course.Namespace).Create(
				context.Background(), newWritablePVC(course), metav1.CreateOptions{},
			)
		}
		if err != nil {
			log.Errorf("Get/Create PVC {%s/%s} fail: %s", course.Namespace, pvcName, err.Error())
			return err
		}
	}

	// Finally, we update the status block of the Foo resource to reflect the current state of the world
	//ingressSvc, err := c.svcLister.
	//	Services(c.config.IngressConfig.Namespace).Get(c.config.IngressConfig.Service)
	err = c.updateCourseSpec(course)
	if err != nil {
		log.Errorf("Update course {%s/%s} spec fail: %s", course.Namespace, course.Name, err.Error())
		return err
	}

	err = c.updateCourseStatus(course, svc)
	if err != nil {
		log.Errorf("Update course {%s/%s} status fail: %s", course.Namespace, course.Name, err.Error())
		return err
	}

	c.recorder.Event(course, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func (c *Controller) updateCourseSpec(course *coursev1alpha1.Course) error {
	courseCopy := course.DeepCopy()

	isMatch := false
	for _, cronExpression := range courseCopy.Spec.Schedule {
		if cron.IsNowMatchCronExpression(cronExpression, "Asia/Taipei") {
			isMatch = true
			break
		}
	}

	// if course is from default classroom, rewrite schedule spec, so we can delete it after one (at most two) hours
	_, exist := c.hasBeenRewrite[courseCopy.Name]
	if !exist && isMatch && strings.HasSuffix(courseCopy.Namespace, PUBLIC_CLASSROOM_SUFFIX) {
		courseCopy.Spec.Schedule = cron.GetConsecutiveTwoHourFromNowCronExpression("Asia/Taipei")
		c.hasBeenRewrite[courseCopy.Name] = true
	}

	_, err := c.sampleclientset.NchcV1alpha1().Courses(course.Namespace).Update(
		context.Background(), courseCopy, metav1.UpdateOptions{})
	return err
}

func (c *Controller) updateCourseStatus(course *coursev1alpha1.Course, svc *corev1.Service) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	courseCopy := course.DeepCopy()

	// Update Course Status
	courseCopy.Status.ServiceName = svc.Name

	// update node port info when access type is NodePort
	if course.Spec.AccessType == coursev1alpha1.AccessTypeNodePort {
		if courseCopy.Status.NodePort == nil {
			courseCopy.Status.NodePort = make(map[string]int32)
		}

		for _, p := range svc.Spec.Ports {
			courseCopy.Status.NodePort[p.Name] = p.NodePort
		}
	}

	// update ingress url info when access type is Ingress
	if course.Spec.AccessType == coursev1alpha1.AccessTypeIngress {

		if courseCopy.Status.SubPath == nil {
			courseCopy.Status.SubPath = make(map[string]string)
		}
		for k, _ := range course.Spec.Port {
			courseCopy.Status.SubPath[k] = k + "-" + course.Name + "." + c.config.IngressBaseUrl
		}
	}

	courseCopy.Status.Accessible = c.isCourseAccessible(svc, course)

	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the Foo resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.sampleclientset.NchcV1alpha1().Courses(course.Namespace).UpdateStatus(
		context.Background(), courseCopy, metav1.UpdateOptions{})
	return err
}

// enqueueCourse takes a Foo resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than Foo.
func (c *Controller) enqueueCourse(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		runtime.HandleError(err)
		return
	}
	c.workqueue.AddRateLimited(key)
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the Foo resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that Foo resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			runtime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			runtime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		log.Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a Foo, we should not do anything more
		// with it.
		if ownerRef.Kind != "Course" {
			return
		}

		course, err := c.courseLister.Courses(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			log.Infof("ignoring orphaned object '%s' of course '%s'", object.GetSelfLink(), ownerRef.Name)
			return
		}

		c.enqueueCourse(course)
		return
	}
}

func (c *Controller) isCourseAccessible(svc *corev1.Service, course *coursev1alpha1.Course) bool {

	// Normally, operator should run inside cluster. Check Pod network connectivity.
	if c.config.IsOutsideCluster == false {
		for _, p := range svc.Spec.Ports {
			svcIp := fmt.Sprintf("%s.%s:%d", svc.Name, svc.Namespace, p.Port)
			timeout := time.Duration(2 * time.Second)
			conn, err := net.DialTimeout("tcp", svcIp, timeout)
			if err != nil {
				return false
			}
			conn.Close()
		}
	}

	// for test only. operator run outside cluster.
	if c.config.IsOutsideCluster {

		baseUrl := c.config.IngressBaseUrl

		switch accessType := course.Spec.AccessType; accessType {
		case coursev1alpha1.AccessTypeNodePort:
			for _, p := range svc.Spec.Ports {
				resp, err := http.Get(fmt.Sprintf("http://%s.%s:%d", course.Name, baseUrl, p.NodePort))
				if err != nil {
					return false
				}

				if resp.StatusCode != 200 {
					return false
				}
			}

		case coursev1alpha1.AccessTypeIngress:
			for _, path := range course.Status.SubPath {
				resp, err := http.Get(fmt.Sprintf("http://%s", path))
				if err != nil {
					return false
				}

				if resp.StatusCode != 200 {
					return false
				}
			}

		default:
			log.Warningf("Unsupport access type: %s", accessType)
		}

	}

	return true
}

// If there is no GPU, we don't configure any resource limit, this case should be in test environment.
// If there are GPU, CPU/Memory are shared evenly among GPUs.
func (c *Controller) detectResourceLimit(kubeclientset kubernetes.Interface) error {

	var factor int64 = 6

	// if configuration has resource limit, read from config
	if c.config.ResourceConfig != nil &&
		c.config.ResourceConfig.Storage != "" && c.config.ResourceConfig.Cpu != "" && c.config.ResourceConfig.Memory != "" {
		log.Infof("Set CPU Limit to %s, Memory Limit to %s, Storage Limit to %s from configuration",
			c.config.ResourceConfig.Cpu, c.config.ResourceConfig.Memory, c.config.ResourceConfig.Storage)
		c.clusterResourceLimit = corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(c.config.ResourceConfig.Cpu),
			corev1.ResourceMemory:           resource.MustParse(c.config.ResourceConfig.Memory),
			corev1.ResourceEphemeralStorage: resource.MustParse(c.config.ResourceConfig.Storage),
		}
		return nil
	}

	if c.config.ResourceConfig != nil && c.config.ResourceConfig.Factor != 0 {
		factor = c.config.ResourceConfig.Factor
	}

	// else, detection from cluster compute node
	nodes, err := kubeclientset.CoreV1().Nodes().List(
		context.Background(),
		// we don't take master node into account when calculate resource limit
		metav1.ListOptions{
			LabelSelector: "node-role.kubernetes.io/master notin ()",
		},
	)
	if err != nil {
		return err
	}

	var minCPU resource.Quantity
	var minMem resource.Quantity
	var minStorage resource.Quantity
	var maxGPU resource.Quantity

	for i, node := range nodes.Items {

		if i == 0 {
			minCPU = node.Status.Allocatable[corev1.ResourceCPU]
			minMem = node.Status.Allocatable[corev1.ResourceMemory]
			minStorage = node.Status.Allocatable[corev1.ResourceEphemeralStorage]
			maxGPU = node.Status.Allocatable["nvidia.com/gpu"]
		}

		a := node.Status.Allocatable["nvidia.com/gpu"]
		if a.Cmp(maxGPU) > 0 {
			maxGPU = a
		}

		b := node.Status.Allocatable[corev1.ResourceCPU]
		if b.Cmp(minCPU) < 0 {
			minCPU = b
		}

		c := node.Status.Allocatable[corev1.ResourceMemory]
		if c.Cmp(minMem) < 0 {
			minMem = c
		}

		d := node.Status.Allocatable[corev1.ResourceEphemeralStorage]
		if d.Cmp(minStorage) < 0 {
			minStorage = d
		}
	}

	if maxGPU.Value() == 0 {
		log.Warningf("Cluster has no GPU, Resource Limit is NOT set. This would be dangerous in production")
		c.clusterResourceLimit = corev1.ResourceList{}
	} else {
		// reserve resource for system-wide pod, so do not assign all resource to jobs having GPU
		_gpu := maxGPU.Value() + factor
		//_gpu := maxGPU.Value() + c.config.ResourceConfig.Factor
		minMem.Set(minMem.Value() / _gpu)
		minCPU.SetMilli(minCPU.Value() * 1000 / _gpu)
		minStorage.Set(minStorage.Value() / _gpu)

		log.Infof("Set CPU Limit to %d, Memory Limit to %d bytes, Storage to %d bytes by auto detection",
			minCPU.Value(), minMem.Value(), minStorage.Value())
		c.clusterResourceLimit = corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(minCPU.String()),
			corev1.ResourceMemory:           resource.MustParse(minMem.String()),
			corev1.ResourceEphemeralStorage: resource.MustParse(minStorage.String()),
		}
	}
	return nil
}
