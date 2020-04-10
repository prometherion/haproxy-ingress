package controller

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_inMemoryLookup_List(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)
	e := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
	}

	g, err := rl.List(endpoint, e)
	assert.Nil(t, err)
	assert.Len(t, g, 0)

	rl.lookupMap[endpoint] = map[namespacedName]namespacedList{
		"default/foo": {"default/ingress-bizz", "default/ingress-buzz"},
	}
	g, err = rl.List(endpoint, e)
	assert.Nil(t, err)
	assert.Len(t, g, 2)
}

func Test_inMemoryLookup_List_NotImplementedClass(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)
	_, err := rl.List(4, &corev1.Endpoints{})
	assert.NotNil(t, err)
}

func Test_inMemoryLookup_Remove(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)
	rl.lookupMap[endpoint] = map[namespacedName]namespacedList{
		"default/foo": {"default/ingress-bizz", "default/ingress-buzz"},
	}

	g, err := rl.Remove(endpoint, &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
	})
	assert.Nil(t, err)
	assert.Len(t, g, 2)
	assert.NotContains(t, rl.lookupMap[endpoint], "default/foo")
}

func Test_inMemoryLookup_Remove_NotImplementedClass(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)
	_, err := rl.Remove(4, &corev1.Endpoints{})
	assert.NotNil(t, err)
}

func Test_inMemoryLookup_Populate(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)

	ingress := v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "42",
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "dontpanic",
			},
			TLS: []v1beta1.IngressTLS{
				{SecretName: "foo"},
				{SecretName: "bar"},
			},
			Rules: []v1beta1.IngressRule{
				{
					IngressRuleValue: v1beta1.IngressRuleValue{
						HTTP: &v1beta1.HTTPIngressRuleValue{
							Paths: []v1beta1.HTTPIngressPath{
								{Backend: v1beta1.IngressBackend{ServiceName: "bizz"}},
								{Backend: v1beta1.IngressBackend{ServiceName: "buzz"}},
							},
						},
					},
				},
			},
		},
	}

	var err error

	err = rl.Populate(ingress)
	assert.Nil(t, err)
	assert.Len(t, rl.lookupMap[endpoint], 3)
	assert.Len(t, rl.lookupMap[service], 3)
	assert.Len(t, rl.lookupMap[secret], 2)
	// inserting second time to ensure no duplicates are there
	err = rl.Populate(ingress)
	assert.Nil(t, err)
	assert.Len(t, rl.lookupMap[endpoint], 3)
	assert.Len(t, rl.lookupMap[service], 3)
	assert.Len(t, rl.lookupMap[secret], 2)
}

func Test_inMemoryLookup_Depopulate(t *testing.T) {
	rl := newRelationshipLookup(&sync.RWMutex{}).(*inMemoryLookup)
	rl.lookupMap[endpoint] = map[namespacedName]namespacedList{
		"default/dontpanic": {"default/ingress-bizz"},
		"default/bizz":      {"default/ingress-bizz", "default/ingress-buzz"},
		"default/buzz":      {"default/42", "default/ingress-buzz"},
	}
	rl.lookupMap[service] = map[namespacedName]namespacedList{
		"default/dontpanic": {"default/ingress-bizz"},
		"default/bizz":      {"default/ingress-bizz", "default/42"},
		"default/buzz":      {"default/ingress-bizz", "default/ingress-buzz"},
	}
	rl.lookupMap[secret] = map[namespacedName]namespacedList{
		"default/foo": {"default/ingress-1-wildcard", "default/42"},
		"default/bar": {"default/ingress-2-wildcard", "default/ingress-2-no_wildcard"},
	}

	ingress := v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "42",
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "dontpanic",
			},
			TLS: []v1beta1.IngressTLS{
				{SecretName: "foo"},
				{SecretName: "bar"},
			},
			Rules: []v1beta1.IngressRule{
				{
					IngressRuleValue: v1beta1.IngressRuleValue{
						HTTP: &v1beta1.HTTPIngressRuleValue{
							Paths: []v1beta1.HTTPIngressPath{
								{Backend: v1beta1.IngressBackend{ServiceName: "bizz"}},
								{Backend: v1beta1.IngressBackend{ServiceName: "buzz"}},
							},
						},
					},
				},
			},
		},
	}

	var err error

	err = rl.Depopulate(ingress)
	assert.Nil(t, err)
	assert.Len(t, rl.lookupMap[endpoint]["default/dontpanic"], 1)
	assert.Len(t, rl.lookupMap[endpoint]["default/bizz"], 2)
	assert.Len(t, rl.lookupMap[endpoint]["default/buzz"], 1)

	assert.Len(t, rl.lookupMap[service]["default/dontpanic"], 1)
	assert.Len(t, rl.lookupMap[service]["default/bizz"], 1)
	assert.Len(t, rl.lookupMap[service]["default/buzz"], 2)

	assert.Len(t, rl.lookupMap[secret]["default/foo"], 1)
	assert.Len(t, rl.lookupMap[secret]["default/bar"], 2)
}
