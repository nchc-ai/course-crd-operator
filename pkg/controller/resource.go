package controller

import (
	"context"
	"fmt"

	"github.com/nchc-ai/course-crd-operator/pkg/util"
	coursev1alpha1 "github.com/nchc-ai/course-crd/pkg/apis/coursecontroller/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func newDeployment(course *coursev1alpha1.Course, defaultResourceLimit corev1.ResourceList) *appsv1.Deployment {

	labels := map[string]string{
		"job_Id": course.Name,
	}

	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}
	// Read-Only dataset volume
	for _, dataset := range course.Spec.Dataset {
		// prepare volume array
		vol := corev1.Volume{
			Name: dataset,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ReadOnly:  true,
					ClaimName: dataset,
				},
			},
		}
		volumes = append(volumes, vol)

		// prepare volumeMount array
		vm := corev1.VolumeMount{
			Name:      dataset,
			MountPath: fmt.Sprintf("/tmp/%s", dataset),
		}
		volumeMounts = append(volumeMounts, vm)
	}

	// mount writable volume if defined in course resource
	// todo: do we need mount multiple writable volume?
	if course.Spec.WritableVolume != nil {
		vol := corev1.Volume{
			Name: "writable",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s", util.StringHashMD5(course.Spec.WritableVolume.Owner)),
				},
			},
		}
		volumes = append(volumes, vol)

		vm := corev1.VolumeMount{
			Name:      "writable",
			MountPath: course.Spec.WritableVolume.MountPoint,
		}
		volumeMounts = append(volumeMounts, vm)
	}

	// shared memory volume (#46)
	vol := corev1.Volume{
		Name: "shm",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium: corev1.StorageMediumMemory,
			},
		},
	}
	volumes = append(volumes, vol)

	vm := corev1.VolumeMount{
		Name:      "shm",
		MountPath: "/dev/shm",
	}
	volumeMounts = append(volumeMounts, vm)

	exposePorts := []corev1.ContainerPort{}
	for _, p := range course.Spec.Port {
		cp := corev1.ContainerPort{
			ContainerPort: p,
		}
		exposePorts = append(exposePorts, cp)
	}

	resourceReq := corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse("100m"),
		corev1.ResourceMemory:           resource.MustParse("100M"),
		corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		"nvidia.com/gpu":                *resource.NewQuantity(int64(course.Spec.Gpu), resource.DecimalSI),
	}

	// if require GPU, add gpu to default resource limit
	if course.Spec.Gpu >= 0 {
		defaultResourceLimit["nvidia.com/gpu"] = *resource.NewQuantity(int64(course.Spec.Gpu), resource.DecimalSI)
	}

	resources := corev1.ResourceRequirements{
		Limits:   defaultResourceLimit,
		Requests: resourceReq,
	}

	envs := []corev1.EnvVar{}

	// if we don't request, set NVIDIA_VISIBLE_DEVICES env to none
	// ref: https://github.com/NVIDIA/k8s-device-plugin/issues/61
	if course.Spec.Gpu == 0 {
		envs = append(envs, corev1.EnvVar{
			Name:  "NVIDIA_VISIBLE_DEVICES",
			Value: "none",
		})
	}

	// if access type is ingress, prepare env for ingress subpath
	//if course.Spec.AccessType == coursev1alpha1.AccessTypeIngress && len(ingressPath) > 0 {
	//	for k, v := range ingressPath {
	//		envs = append(envs, corev1.EnvVar{
	//			Name:  strings.ToUpper(k),
	//			Value: v,
	//		})
	//	}
	//}

	// prepare container Spec

	psc := corev1.PodSecurityContext{}
	if course.Spec.WritableVolume != nil {
		psc.RunAsUser = util.Int64Ptr(course.Spec.WritableVolume.Uid)
		psc.RunAsGroup = util.Int64Ptr(course.Spec.WritableVolume.Uid)
	}

	podSpec := corev1.PodSpec{
		SecurityContext: &psc,
		Containers: []corev1.Container{
			{
				Name:         course.Name,
				Image:        course.Spec.Image,
				Ports:        exposePorts,
				Resources:    resources,
				VolumeMounts: volumeMounts,
				Env:          envs,
			},
		},
		Volumes: volumes,
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      course.Name,
			Namespace: course.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(course, schema.GroupVersionKind{
					Group:   coursev1alpha1.SchemeGroupVersion.Group,
					Version: coursev1alpha1.SchemeGroupVersion.Version,
					Kind:    "Course",
				}),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: util.Int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
}

func newService(course *coursev1alpha1.Course, svcName string) *corev1.Service {

	selector := map[string]string{
		"job_Id": course.Name,
	}

	var ports []corev1.ServicePort
	for name, p := range course.Spec.Port {
		sp := corev1.ServicePort{
			Name: name,
			Port: p,
			TargetPort: intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: p,
			},
		}
		ports = append(ports, sp)
	}

	svcSpec := corev1.ServiceSpec{
		Selector: selector,
		Ports:    ports,
	}

	if course.Spec.AccessType == coursev1alpha1.AccessTypeNodePort {
		svcSpec.Type = corev1.ServiceTypeNodePort
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: course.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(course, schema.GroupVersionKind{
					Group:   coursev1alpha1.SchemeGroupVersion.Group,
					Version: coursev1alpha1.SchemeGroupVersion.Version,
					Kind:    "Course",
				}),
			},
		},

		Spec: svcSpec,
	}
}

func getOrCreateService(controller *Controller, course *coursev1alpha1.Course) (*corev1.Service, error) {
	svcs, err := controller.svcLister.Services(course.Namespace).List(labels.NewSelector())

	if err != nil {
		fmt.Println(err.Error())
		return nil, err
	}

	for _, svc := range svcs {
		if metav1.IsControlledBy(svc, course) {
			return svc, nil
		}
	}

	svc, err := controller.kubeclientset.CoreV1().
		Services(course.Namespace).Create(
		context.Background(),
		newService(course, util.SvcNameGen()),
		metav1.CreateOptions{},
	)

	if err != nil {
		return nil, err
	}

	return svc, nil
}

func newWritablePVC(course *coursev1alpha1.Course) *corev1.PersistentVolumeClaim {

	pvcName := fmt.Sprintf("%s", util.StringHashMD5(course.Spec.WritableVolume.Owner))

	resourceRequest := corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse("1Gi"),
	}
	resources := corev1.VolumeResourceRequirements{
		Requests: resourceRequest,
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources:        resources,
			StorageClassName: &course.Spec.WritableVolume.StorageClass,
			//StorageClassName: util.String2Ptr("nchc-ai-nfs"),
		},
	}
}

func newIngress(course *coursev1alpha1.Course, baseUrl, svcName, ingClass string) *networkv1.Ingress {

	ingressRule := []networkv1.IngressRule{}
	hosts := []string{}

	implementationSpecificPathType := networkv1.PathTypeImplementationSpecific
	for k, v := range course.Spec.Port {
		h := k + "-" + course.Name + "." + baseUrl
		ingressRule = append(ingressRule, networkv1.IngressRule{
			Host: h,
			IngressRuleValue: networkv1.IngressRuleValue{
				HTTP: &networkv1.HTTPIngressRuleValue{
					Paths: []networkv1.HTTPIngressPath{
						{
							PathType: &implementationSpecificPathType,
							Backend: networkv1.IngressBackend{
								Service: &networkv1.IngressServiceBackend{
									Name: svcName,
									Port: networkv1.ServiceBackendPort{
										Number: v,
									},
								},
							},
						},
					},
				},
			},
		})

		hosts = append(hosts, h)
	}

	tls := []networkv1.IngressTLS{
		{
			Hosts:      hosts,
			SecretName: TlsSecretName,
		},
	}

	return &networkv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			//Annotations: map[string]string{
			//	"kubernetes.io/ingress.class": ingClass,
			//},
			Name:      course.Name,
			Namespace: course.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(course, schema.GroupVersionKind{
					Group:   coursev1alpha1.SchemeGroupVersion.Group,
					Version: coursev1alpha1.SchemeGroupVersion.Version,
					Kind:    "Course",
				}),
			},
		},
		Spec: networkv1.IngressSpec{
			IngressClassName: util.String2Ptr(ingClass),
			Rules:            ingressRule,
			TLS:              tls,
		},
	}
}

func getOrCreateIngress(controller *Controller, course *coursev1alpha1.Course, svcName string) (*networkv1.Ingress, error) {
	ingress, err := controller.ingressLister.Ingresses(course.Namespace).List(labels.NewSelector())

	if err != nil {
		fmt.Println(err.Error())
		return nil, err
	}

	for _, ing := range ingress {
		if metav1.IsControlledBy(ing, course) {
			return ing, nil
		}
	}

	ing, err := controller.kubeclientset.NetworkingV1().Ingresses(course.Namespace).
		Create(
			context.Background(),
			newIngress(course, controller.config.IngressBaseUrl, svcName, controller.config.IngressClass),
			metav1.CreateOptions{},
		)

	if err != nil {
		return nil, err
	}

	return ing, nil
}
