package util

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	finalizerKey = "hci.wjyl.com/"
)

func HasFinalizer(obj metav1.Object, name string) bool {
	finalizerKey := constructFinalizerKey(name)
	finalizers := obj.GetFinalizers()
	for _, finalizer := range finalizers {
		if finalizer == finalizerKey {
			return true
		}
	}
	return false
}

func AddFinalizer(obj metav1.Object, name string) {
	if HasFinalizer(obj, name) {
		return
	}
	obj.SetFinalizers(append(obj.GetFinalizers(), constructFinalizerKey(name)))
}

func RemoveFinalizer(obj metav1.Object, name string) {
	if !HasFinalizer(obj, name) {
		return
	}

	finalizerKey := constructFinalizerKey(name)
	finalizers := obj.GetFinalizers()

	var newFinalizers []string
	for k, v := range finalizers {
		if v != finalizerKey {
			continue
		}
		newFinalizers = append(finalizers[:k], finalizers[k+1:]...)
	}

	obj.SetFinalizers(newFinalizers)
}

func constructFinalizerKey(name string) string {
	return finalizerKey + name
}

func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
