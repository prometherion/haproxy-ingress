package controller

import (
	"fmt"
	"strings"
	"sync"

	extv1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Class int

const (
	_              = iota
	endpoint Class = iota
	service
	secret
)

type namespacedName string

type namespacedList []namespacedName

func (n namespacedList) hydrate() (ingressList []extv1beta1.Ingress) {
	for _, name := range n {
		n := strings.Split(string(name), "/")
		ingressList = append(ingressList, extv1beta1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: n[0],
				Name:      n[1],
			},
		})
	}
	return
}

type lookupMap map[Class]map[namespacedName]namespacedList

type Lookup interface {
	Populate(ingress extv1beta1.Ingress) error
	Depopulate(ingress extv1beta1.Ingress) error
	Remove(class Class, object metav1.Object) ([]extv1beta1.Ingress, error)
	List(class Class, object metav1.Object) (ingressList []extv1beta1.Ingress, err error)
}

type inMemoryLookup struct {
	mutex     *sync.RWMutex
	lookupMap lookupMap
}

func (r inMemoryLookup) namespacedName(object metav1.Object) namespacedName {
	return namespacedName(fmt.Sprintf("%s/%s", object.GetNamespace(), object.GetName()))
}

func (r inMemoryLookup) namespacedNameFromIngress(ingress extv1beta1.Ingress, resourceName string) namespacedName {
	return namespacedName(fmt.Sprintf("%s/%s", ingress.GetNamespace(), resourceName))
}

func (r inMemoryLookup) getResourceList(ing extv1beta1.Ingress) (epList, svcList, secretList []namespacedName) {
	if b := ing.Spec.Backend; b != nil {
		svcList = append(svcList, r.namespacedNameFromIngress(ing, b.ServiceName))
	}
	for _, i := range ing.Spec.Rules {
		if i.HTTP == nil {
			continue
		}
		for _, p := range i.HTTP.Paths {
			svcList = append(svcList, r.namespacedNameFromIngress(ing, p.Backend.ServiceName))
		}
	}

	epList = svcList

	for _, s := range ing.Spec.TLS {
		secretList = append(secretList, r.namespacedNameFromIngress(ing, s.SecretName))
	}

	return
}

func (r *inMemoryLookup) prependFrom(class Class, resourceName, ingressKey namespacedName) {
	var f int
	for i, ing := range r.lookupMap[class][resourceName] {
		if ing == ingressKey {
			f = i
			r.lookupMap[class][resourceName] = append(r.lookupMap[class][resourceName][:f], r.lookupMap[class][resourceName][f+1:]...)
			break
		}
	}
}

func (r *inMemoryLookup) appendTo(class Class, resourceName, ingressKey namespacedName) {
	if _, ok := r.lookupMap[class][resourceName]; !ok {
		r.lookupMap[class][resourceName] = make(namespacedList, 0)
	}
	for _, i := range r.lookupMap[class][resourceName] {
		// just avoiding to insert duplicated ingresses
		if i == ingressKey {
			return
		}
	}
	r.lookupMap[class][resourceName] = append(r.lookupMap[class][resourceName], ingressKey)
}

func (r *inMemoryLookup) Depopulate(ingress extv1beta1.Ingress) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	ingKey := r.namespacedName(&ingress)
	epList, svcNameList, secretNameList := r.getResourceList(ingress)

	for _, i := range epList {
		r.prependFrom(endpoint, i, ingKey)
	}
	for _, i := range svcNameList {
		r.prependFrom(service, i, ingKey)
	}
	for _, i := range secretNameList {
		r.prependFrom(secret, i, ingKey)
	}

	return nil
}

func (r *inMemoryLookup) Populate(ingress extv1beta1.Ingress) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	ingKey := r.namespacedName(&ingress)
	epList, svcNameList, secretNameList := r.getResourceList(ingress)

	for _, i := range epList {
		r.appendTo(endpoint, i, ingKey)
	}
	for _, i := range svcNameList {
		r.appendTo(service, i, ingKey)
	}
	for _, i := range secretNameList {
		r.appendTo(secret, i, ingKey)
	}

	return nil
}

func (r inMemoryLookup) isImplementedClass(class Class) (err error) {
	if class > secret {
		err = fmt.Errorf("class %v not implemented", class)
	}
	return
}

func (r *inMemoryLookup) Remove(class Class, object metav1.Object) (ingressList []extv1beta1.Ingress, err error) {
	if err := r.isImplementedClass(class); err != nil {
		return nil, err
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()

	n := r.namespacedName(object)
	if m, ok := r.lookupMap[class][n]; ok {
		ingressList = m.hydrate()
		delete(r.lookupMap[class], n)
	}

	return
}

func (r inMemoryLookup) List(class Class, object metav1.Object) (ingressList []extv1beta1.Ingress, err error) {
	if err := r.isImplementedClass(class); err != nil {
		return nil, err
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	n := r.namespacedName(object)
	if m, ok := r.lookupMap[class][n]; ok {
		ingressList = m.hydrate()
	}

	return
}

func newRelationshipLookup(m *sync.RWMutex) Lookup {
	return &inMemoryLookup{
		mutex: m,
		lookupMap: lookupMap{
			endpoint: make(map[namespacedName]namespacedList),
			service:  make(map[namespacedName]namespacedList),
			secret:   make(map[namespacedName]namespacedList),
		},
	}
}
