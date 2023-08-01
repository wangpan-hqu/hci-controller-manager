/*
Copyright 2022.

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

package hci

import (
	"context"
	"fmt"
	lhcontroller "github.com/longhorn/longhorn-manager/controller"
	"github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	lhmanager "github.com/longhorn/longhorn-manager/manager"
	lhtypes "github.com/longhorn/longhorn-manager/types"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/ref"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hciv1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
)

const (
	finalizerName = "hci.wjyl.com/vm-image-controller"

	optionBackingImageName = "backingImage"
	optionMigratable       = "migratable"
)

// VirtualMachineImageReconciler reconciles a VirtualMachineImage object
type VirtualMachineImageReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	HttpClient http.Client
}

//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachineimages,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachineimages/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachineimages/finalizers,verbs=update
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims/finalizers,verbs=update

func (r *VirtualMachineImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	tgt := &hciv1.VirtualMachineImage{}
	if err := r.Get(ctx, req.NamespacedName, tgt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if tgt.DeletionTimestamp != nil {
		return r.onRemove(ctx, tgt)
	}

	result, err := r.onChange(ctx, tgt)
	return result, err
}

func (r *VirtualMachineImageReconciler) onChange(ctx context.Context, image *hciv1.VirtualMachineImage) (ctrl.Result, error) {
	//logger := log.FromContext(ctx)
	if image == nil || image.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	if hciv1.ImageInitialized.GetStatus(image) == "" {
		return r.initialize(image)
	} else if image.Spec.URL != image.Status.AppliedURL {
		// URL is changed, recreate the storageclass and backingimage
		if err := r.deleteBackingImageAndStorageClass(image); err != nil {
			return ctrl.Result{}, err
		}
		return r.initialize(image)
	}
	return ctrl.Result{}, nil
}

func (r *VirtualMachineImageReconciler) onRemove(ctx context.Context, image *hciv1.VirtualMachineImage) (ctrl.Result, error) {
	//logger := log.FromContext(ctx)
	if image == nil {
		return ctrl.Result{}, nil
	}
	sc := &storagev1.StorageClass{
		TypeMeta:   metav1.TypeMeta{Kind: "StorageClass"},
		ObjectMeta: metav1.ObjectMeta{Name: util.GetImageStorageClassName(image.Name)},
	}
	if err := r.Delete(context.TODO(), sc); !errors.IsNotFound(err) && err != nil {
		return ctrl.Result{}, err
	}
	bi := &v1beta1.BackingImage{
		TypeMeta:   metav1.TypeMeta{Kind: "BackingImage"},
		ObjectMeta: metav1.ObjectMeta{Namespace: util.LonghornSystemNamespaceName, Name: util.GetBackingImageName(image)},
	}
	if err := r.Delete(context.TODO(), bi); !errors.IsNotFound(err) && err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(image, finalizerName)
	if err := r.Update(ctx, image); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VirtualMachineImageReconciler) initialize(image *hciv1.VirtualMachineImage) (ctrl.Result, error) {
	if !util.ContainsString(image.GetFinalizers(), finalizerName) {
		controllerutil.AddFinalizer(image, finalizerName)
		if err := r.Update(context.TODO(), image); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.createBackingImage(image); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	if err := r.createStorageClass(image); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	toUpdate := image.DeepCopy()
	toUpdate.Status.AppliedURL = toUpdate.Spec.URL
	toUpdate.Status.StorageClassName = util.GetImageStorageClassName(image.Name)

	if image.Spec.SourceType == hciv1.VirtualMachineImageSourceTypeDownload {
		resp, err := r.HttpClient.Head(image.Spec.URL)
		if err != nil {
			hciv1.ImageInitialized.False(toUpdate)
			hciv1.ImageInitialized.Message(toUpdate, err.Error())
			return ctrl.Result{}, r.Update(context.TODO(), toUpdate)
		}
		defer resp.Body.Close()

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
			hciv1.ImageInitialized.False(toUpdate)
			hciv1.ImageInitialized.Message(toUpdate, fmt.Sprintf("got %d status code from %s", resp.StatusCode, image.Spec.URL))
			return ctrl.Result{}, r.Update(context.TODO(), toUpdate)
		}

		if resp.ContentLength > 0 {
			toUpdate.Status.Size = resp.ContentLength
		}
	} else {
		toUpdate.Status.Progress = 0
	}

	hciv1.ImageImported.Unknown(toUpdate)
	hciv1.ImageImported.Reason(toUpdate, "Importing")
	hciv1.ImageInitialized.True(toUpdate)
	hciv1.ImageInitialized.Reason(toUpdate, "Initialized")

	return ctrl.Result{}, r.Update(context.TODO(), toUpdate)
}

func (r *VirtualMachineImageReconciler) createBackingImage(image *hciv1.VirtualMachineImage) error {
	bi := &v1beta1.BackingImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      util.GetBackingImageName(image),
			Namespace: util.LonghornSystemNamespaceName,
			Annotations: map[string]string{
				util.AnnotationImageID: ref.Construct(image.Namespace, image.Name),
			},
		},
		Spec: v1beta1.BackingImageSpec{
			SourceType:       v1beta1.BackingImageDataSourceType(image.Spec.SourceType),
			SourceParameters: map[string]string{},
			Checksum:         image.Spec.Checksum,
		},
	}
	if image.Spec.SourceType == hciv1.VirtualMachineImageSourceTypeDownload {
		bi.Spec.SourceParameters[v1beta1.DataSourceTypeDownloadParameterURL] = image.Spec.URL
	}

	if image.Spec.SourceType == hciv1.VirtualMachineImageSourceTypeExportVolume {
		pvc := &corev1.PersistentVolumeClaim{}
		key := types.NamespacedName{Namespace: image.Spec.PVCNamespace, Name: image.Spec.PVCName}
		err := r.Get(context.TODO(), key, pvc)
		if err != nil {
			return fmt.Errorf("failed to get pvc %s/%s, error: %s", image.Spec.PVCName, image.Namespace, err.Error())
		}

		bi.Spec.SourceParameters[lhcontroller.DataSourceTypeExportFromVolumeParameterVolumeName] = pvc.Spec.VolumeName
		bi.Spec.SourceParameters[lhmanager.DataSourceTypeExportFromVolumeParameterExportType] = lhmanager.DataSourceTypeExportFromVolumeParameterExportTypeRAW
	}

	err := r.Create(context.TODO(), bi)
	return err
}

func (r *VirtualMachineImageReconciler) createStorageClass(image *hciv1.VirtualMachineImage) error {
	recliamPolicy := corev1.PersistentVolumeReclaimDelete
	volumeBindingMode := storagev1.VolumeBindingImmediate
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: util.GetImageStorageClassName(image.Name),
		},
		Provisioner:          lhtypes.LonghornDriverName,
		ReclaimPolicy:        &recliamPolicy,
		AllowVolumeExpansion: pointer.BoolPtr(true),
		VolumeBindingMode:    &volumeBindingMode,
		Parameters:           util.GetImageStorageClassParameters(image),
		//Parameters: map[string]string{
		//	lhtypes.OptionNumberOfReplicas:    "3",
		//	lhtypes.OptionStaleReplicaTimeout: "30",
		//	optionMigratable:                  "true",
		//},
	}

	err := r.Create(context.TODO(), sc)
	return err
}

func (r *VirtualMachineImageReconciler) deleteBackingImage(image *hciv1.VirtualMachineImage) error {
	bi := &v1beta1.BackingImage{
		TypeMeta:   metav1.TypeMeta{Kind: "BackingImage"},
		ObjectMeta: metav1.ObjectMeta{Namespace: util.LonghornSystemNamespaceName, Name: util.GetBackingImageName(image)},
	}
	if err := r.Delete(context.TODO(), bi); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *VirtualMachineImageReconciler) deleteStorageClass(image *hciv1.VirtualMachineImage) error {
	sc := &storagev1.StorageClass{
		TypeMeta:   metav1.TypeMeta{Kind: "StorageClass"},
		ObjectMeta: metav1.ObjectMeta{Name: util.GetImageStorageClassName(image.Name)},
	}
	if err := r.Delete(context.TODO(), sc); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *VirtualMachineImageReconciler) deleteBackingImageAndStorageClass(image *hciv1.VirtualMachineImage) error {
	if err := r.deleteBackingImage(image); err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err := r.deleteStorageClass(image); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hciv1.VirtualMachineImage{}).
		Complete(r)
}
