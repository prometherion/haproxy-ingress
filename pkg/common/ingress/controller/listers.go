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
	"fmt"
	"reflect"

	"github.com/golang/glog"
	apiv1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/jcmoraisjr/haproxy-ingress/pkg/common/ingress"
)

type cacheController struct {
	lookup Lookup

	Ingress   cache.Controller
	Endpoint  cache.Controller
	Service   cache.Controller
	Node      cache.Controller
	Secret    cache.Controller
	ConfigMap cache.Controller
	Pod       cache.Controller
}

func (c *cacheController) Run(stopCh chan struct{}) {
	go c.Ingress.Run(stopCh)
	go c.Endpoint.Run(stopCh)
	go c.Service.Run(stopCh)
	go c.Node.Run(stopCh)
	go c.Secret.Run(stopCh)
	go c.ConfigMap.Run(stopCh)
	go c.Pod.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the queue is started
	if !cache.WaitForCacheSync(stopCh,
		c.Ingress.HasSynced,
		c.Endpoint.HasSynced,
		c.Service.HasSynced,
		c.Node.HasSynced,
		c.Secret.HasSynced,
		c.ConfigMap.HasSynced,
		c.Pod.HasSynced,
	) {
		runtime.HandleError(fmt.Errorf("timed out waiting for caches to sync"))
	}
}

func (ic *GenericController) createListers(disableNodeLister bool) (*ingress.StoreLister, *cacheController) {
	lister := &ingress.StoreLister{}
	lister.Secret.Client = ic.cfg.Client
	lister.ConfigMap.Client = ic.cfg.Client

	controller := &cacheController{}

	si := informers.NewSharedInformerFactoryWithOptions(ic.cfg.Client, ic.cfg.ResyncPeriod, func() informers.SharedInformerOption {
		if ic.cfg.ForceNamespaceIsolation && ic.cfg.WatchNamespace != apiv1.NamespaceAll {
			return informers.WithNamespace(ic.cfg.WatchNamespace)
		}
		return informers.WithTweakListOptions(nil)
	}())

	ingressInformer := si.Extensions().V1beta1().Ingresses()
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addIng := obj.(*extensions.Ingress)
			if !ic.IsValidClass(addIng) {
				a, _ := addIng.Annotations[IngressClassKey]
				glog.Infof("ignoring add for ingress %s/%s based on annotation %s with value %s",
					addIng.Namespace, addIng.Name, IngressClassKey, a)
				return
			}
			if err := ic.lookup.Populate(*addIng); err != nil {
				glog.Warningf("cannot populate the in-memory lookup map: %s", err.Error())
			}
			ic.recorder.Eventf(addIng, apiv1.EventTypeNormal, "CREATE", fmt.Sprintf("Ingress %s/%s", addIng.Namespace, addIng.Name))
			ic.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			delIng, ok := obj.(*extensions.Ingress)
			if !ok {
				// If we reached here it means the ingress was deleted but its final state is unrecorded.
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					glog.Errorf("couldn't get object from tombstone %#v", obj)
					return
				}
				delIng, ok = tombstone.Obj.(*extensions.Ingress)
				if !ok {
					glog.Errorf("Tombstone contained object that is not an Ingress: %#v", obj)
					return
				}
			}
			if !ic.IsValidClass(delIng) {
				glog.Infof("ignoring delete for ingress %s/%s based on annotation %s",
					delIng.Namespace, delIng.Name, IngressClassKey)
				return
			}
			if err := ic.lookup.Depopulate(*delIng); err != nil {
				glog.Warningf("cannot depopulate the in-memory lookup map: %s", err.Error())
			}
			ic.recorder.Eventf(delIng, apiv1.EventTypeNormal, "DELETE", fmt.Sprintf("Ingress %s/%s", delIng.Namespace, delIng.Name))
			ic.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			oldIng := old.(*extensions.Ingress)
			curIng := cur.(*extensions.Ingress)
			validOld := ic.IsValidClass(oldIng)
			validCur := ic.IsValidClass(curIng)
			if !validOld && validCur {
				glog.Infof("creating ingress %v based on annotation %v", curIng.Name, IngressClassKey)
				ic.recorder.Eventf(curIng, apiv1.EventTypeNormal, "CREATE", fmt.Sprintf("Ingress %s/%s", curIng.Namespace, curIng.Name))
				if err := ic.lookup.Populate(*curIng); err != nil {
					glog.Warningf("cannot populate the in-memory lookup map: %s", err.Error())
				}
			} else if validOld && !validCur {
				glog.Infof("removing ingress %v based on annotation %v", curIng.Name, IngressClassKey)
				ic.recorder.Eventf(curIng, apiv1.EventTypeNormal, "DELETE", fmt.Sprintf("Ingress %s/%s", curIng.Namespace, curIng.Name))
				if err := ic.lookup.Depopulate(*oldIng); err != nil {
					glog.Warningf("cannot depopulate the in-memory lookup map: %s", err.Error())
				}
			} else if validCur && !reflect.DeepEqual(old, cur) {
				if err := ic.lookup.Depopulate(*curIng); err != nil {
					glog.Warningf("cannot depopulate the in-memory lookup map: %s", err.Error())
				}
				if err := ic.lookup.Populate(*curIng); err != nil {
					glog.Warningf("cannot populate the in-memory lookup map: %s", err.Error())
				}
				ic.recorder.Eventf(curIng, apiv1.EventTypeNormal, "UPDATE", fmt.Sprintf("Ingress %s/%s", curIng.Namespace, curIng.Name))
			}

			ic.syncQueue.Enqueue(cur)
		},
	})
	lister.Ingress.Lister, controller.Ingress = ingressInformer.Lister(), ingressInformer.Informer()

	secretInformer := si.Core().V1().Secrets()
	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			sec, ok := obj.(*apiv1.Secret)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", obj)
				return
			}
			il, err := ic.lookup.List(secret, sec)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to secret %s/%s from the in-memory lookup map: %s",
					sec.Namespace, sec.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			if reflect.DeepEqual(old, cur) {
				return
			}
			sec, ok := cur.(*apiv1.Secret)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", cur)
				return
			}
			key := fmt.Sprintf("%v/%v", sec.Namespace, sec.Name)
			ic.syncSecret(key)

			il, err := ic.lookup.List(secret, sec)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to secret %s/%s from the in-memory lookup map: %s",
					sec.Namespace, sec.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
		DeleteFunc: func(obj interface{}) {
			sec, ok := obj.(*apiv1.Secret)
			if !ok {
				// If we reached here it means the secret was deleted but its final state is unrecorded.
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					glog.Errorf("couldn't get object from tombstone %#v", obj)
					return
				}
				sec, ok = tombstone.Obj.(*apiv1.Secret)
				if !ok {
					glog.Errorf("Tombstone contained object that is not a Secret: %#v", obj)
					return
				}
			}
			key := fmt.Sprintf("%v/%v", sec.Namespace, sec.Name)
			ic.sslCertTracker.DeleteAll(key)
			il, err := ic.lookup.Remove(secret, sec)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to secret %s from the in-memory lookup map: %s",
					key, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
	})
	lister.Secret.Lister, controller.Secret = secretInformer.Lister(), secretInformer.Informer()

	endpointInformer := si.Core().V1().Endpoints()
	endpointInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ep, ok := obj.(*apiv1.Endpoints)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", obj)
				return
			}
			il, err := ic.lookup.List(endpoint, ep)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to endpoint %s/%s from the in-memory lookup map: %s",
					ep.Namespace, ep.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
		DeleteFunc: func(obj interface{}) {
			ep, ok := obj.(*apiv1.Endpoints)
			if !ok {
				// If we reached here it means the secret was deleted but its final state is unrecorded.
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					glog.Errorf("couldn't get object from tombstone %#v", obj)
					return
				}
				ep, ok = tombstone.Obj.(*apiv1.Endpoints)
				if !ok {
					glog.Errorf("Tombstone contained object that is not an Endpoint: %#v", obj)
					return
				}
			}
			il, err := ic.lookup.Remove(endpoint, ep)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to endpoint %s/%s from the in-memory lookup map: %s",
					ep.Namespace, ep.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			oep, ok := old.(*apiv1.Endpoints)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", old)
				return
			}
			ocur, ok := cur.(*apiv1.Endpoints)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", cur)
				return
			}

			if reflect.DeepEqual(ocur.Subsets, oep.Subsets) {
				return
			}

			var il []extensions.Ingress
			var err error

			il, err = ic.lookup.List(endpoint, ocur)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to endpoint %s/%s from the in-memory lookup map: %s",
					oep.Namespace, oep.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
	})
	lister.Endpoint.Lister, controller.Endpoint = endpointInformer.Lister(), endpointInformer.Informer()

	cmInformer := si.Core().V1().ConfigMaps()
	cmInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			upCmap := obj.(*apiv1.ConfigMap)
			mapKey := fmt.Sprintf("%s/%s", upCmap.Namespace, upCmap.Name)
			if mapKey == ic.cfg.ConfigMapName {
				glog.V(2).Infof("adding configmap %v to backend", mapKey)
				ic.cfg.Backend.SetConfig(upCmap)
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				upCmap := cur.(*apiv1.ConfigMap)
				mapKey := fmt.Sprintf("%s/%s", upCmap.Namespace, upCmap.Name)
				if mapKey == ic.cfg.ConfigMapName {
					glog.V(2).Infof("updating configmap backend (%v)", mapKey)
					ic.cfg.Backend.SetConfig(upCmap)
				}
				// updates to configuration configmaps can trigger an update
				if mapKey == ic.cfg.ConfigMapName || mapKey == ic.cfg.TCPConfigMapName {
					ic.recorder.Eventf(upCmap, apiv1.EventTypeNormal, "UPDATE", fmt.Sprintf("ConfigMap %v", mapKey))
					// enqueuing a nil just to update the config map in order
					// to perform the live reload of the HAProxy configuration.
					ic.syncQueue.Enqueue(nil)
				}
			}
		},
	})
	lister.ConfigMap.Lister, controller.ConfigMap = cmInformer.Lister(), cmInformer.Informer()

	serviceInformer := si.Core().V1().Services()
	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			if reflect.DeepEqual(oldObj, newObj) {
				return
			}
			newSvc, ok := newObj.(*apiv1.Service)
			if !ok {
				glog.Errorf("wrong object from the cache: %v", newObj)
				return
			}

			var il []extensions.Ingress
			var err error

			il, err = ic.lookup.List(service, newSvc)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to service %s/%s from the in-memory lookup map: %s",
					newSvc.Namespace, newSvc.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
		DeleteFunc: func(obj interface{}) {
			svc, ok := obj.(*apiv1.Service)
			if !ok {
				// If we reached here it means the secret was deleted but its final state is unrecorded.
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					glog.Errorf("couldn't get object from tombstone %#v", obj)
					return
				}
				svc, ok = tombstone.Obj.(*apiv1.Service)
				if !ok {
					glog.Errorf("Tombstone contained object that is not an Endpoint: %#v", obj)
					return
				}
			}

			var il []extensions.Ingress
			var err error

			il, err = ic.lookup.Remove(service, svc)
			if err != nil {
				glog.Warningf(
					"cannot retrieve ingress resources related to service %s/%s from the in-memory lookup map: %s",
					svc.Namespace, svc.Name, err.Error(),
				)
				return
			}
			for _, i := range il {
				ic.syncQueue.Enqueue(i)
			}
		},
	})
	lister.Service.Lister, controller.Service = serviceInformer.Lister(), serviceInformer.Informer()

	// TODO(prometherion): https://github.com/jcmoraisjr/haproxy-ingress/issues/547#issuecomment-611929802
	// should be removed
	podInformer := si.Core().V1().Pods()
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			ic.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			oldPod := old.(*apiv1.Pod)
			newPod := cur.(*apiv1.Pod)
			if oldPod.DeletionTimestamp != newPod.DeletionTimestamp {
				ic.syncQueue.Enqueue(cur)
			}
		},
	})
	lister.Pod.Lister, controller.Pod = podInformer.Lister(), podInformer.Informer()

	// TODO(prometherion): https://github.com/jcmoraisjr/haproxy-ingress/issues/547#issuecomment-610380342
	// should be removed: old controller inheritance
	if disableNodeLister {
		cs := fake.NewSimpleClientset()
		si = informers.NewSharedInformerFactory(cs, 0)
	}
	nodeInformer := si.Core().V1().Nodes()
	lister.Node.Lister, controller.Node = nodeInformer.Lister(), nodeInformer.Informer()

	return lister, controller
}
