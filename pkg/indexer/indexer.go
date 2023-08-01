package indexer

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type PersistentVolumeClaimCache struct {
	Indexer cache.Indexer
	//resource schema.GroupResource
}

const (
	PVCByVMIndex = "hci.wjyl.com/owned-by"
)

type PersistentVolumeClaimIndexer func(obj *corev1.PersistentVolumeClaim) ([]string, error)

func (c *PersistentVolumeClaimCache) AddIndexer(indexName string, indexer PersistentVolumeClaimIndexer) {
	utilruntime.Must(c.Indexer.AddIndexers(map[string]cache.IndexFunc{
		indexName: func(obj interface{}) (strings []string, e error) {
			return indexer(obj.(*corev1.PersistentVolumeClaim))
		},
	}))

}

func (c *PersistentVolumeClaimCache) GetByIndex(indexName, key string) (result []*corev1.PersistentVolumeClaim, err error) {
	objs, err := c.Indexer.ByIndex(indexName, key)
	if err != nil {
		return nil, err
	}
	result = make([]*corev1.PersistentVolumeClaim, 0, len(objs))
	for _, obj := range objs {
		result = append(result, obj.(*corev1.PersistentVolumeClaim))
	}
	return result, nil
}

func AnnotationsIndexFunc(obj interface{}) ([]string, error) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return []string{""}, fmt.Errorf("object has no meta: %v", err)
	}
	annotations := m.GetAnnotations()
	return []string{annotations[PVCByVMIndex]}, nil
}
