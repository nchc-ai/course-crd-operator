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

package main

import (
	"flag"
	"time"

	"github.com/nchc-ai/course-crd-operator/pkg/controller"
	"github.com/nchc-ai/course-crd-operator/pkg/model"
	"github.com/nchc-ai/course-crd-operator/pkg/util"

	log "github.com/golang/glog"
	"github.com/nchc-ai/course-crd-operator/pkg/signals"
	clientset "github.com/nchc-ai/course-crd/pkg/client/clientset/versioned"
	informers "github.com/nchc-ai/course-crd/pkg/client/informers/externalversions"
	"github.com/spf13/viper"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

var (
	configfile string
)

func main() {
	flag.Parse()

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	// Read config file
	config, err := ReadConfig(configfile)
	if err != nil {
		log.Fatalf("Unable to read configure file: %s", err.Error())
	}

	confObj, err := model.NewConfig(config)

	if err != nil {
		log.Fatalf("Error Unmarshal config json file: %s", err.Error())
	}

	cfg, err := util.GetConfig(confObj.IsOutsideCluster, confObj.Kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	exampleClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("Error building example clientset: %s", err.Error())
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, time.Second*30)
	exampleInformerFactory := informers.NewSharedInformerFactory(exampleClient, time.Second*30)

	controller := controller.NewController(kubeClient, exampleClient,
		kubeInformerFactory.Apps().V1().Deployments(),
		kubeInformerFactory.Core().V1().Services(),
		kubeInformerFactory.Core().V1().PersistentVolumeClaims(),
		kubeInformerFactory.Networking().V1().Ingresses(),
		exampleInformerFactory.Nchc().V1alpha1().Courses(),
		confObj)

	go kubeInformerFactory.Start(stopCh)
	go exampleInformerFactory.Start(stopCh)

	if err = controller.Init(); err != nil {
		log.Fatalf("Error init controller: %s", err.Error())
	}

	if err = controller.Run(2, stopCh); err != nil {
		log.Fatalf("Error running controller: %s", err.Error())
	}
}

func init() {
	flag.StringVar(&configfile, "config", "", "Path to a config file.")
}

func ReadConfig(fileConfig string) (*viper.Viper, error) {
	viper := viper.New()
	viper.SetConfigType("json")

	if fileConfig == "" {
		viper.SetConfigName("config")
		viper.AddConfigPath("/etc/course-operator")
	} else {
		viper.SetConfigFile(fileConfig)
	}

	// overwrite by file
	err := viper.ReadInConfig()
	if err != nil {
		return nil, err
	}

	return viper, nil
}
