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
	lhv1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	hciv1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/ref"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BackingImageReconciler reconciles a VirtualMachineImage object
type BackingImageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=longhorn.io,resources=backingimages,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=longhorn.io,resources=backingimages/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=longhorn.io,resources=backingimages/finalizers,verbs=update

func (r *BackingImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tgt := &lhv1.BackingImage{}
	if err := r.Get(ctx, req.NamespacedName, tgt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if tgt.DeletionTimestamp != nil {
		//return r.onRemove(ctx, tgt)
	}

	result, err := r.onChange(ctx, tgt)
	return result, err
}

func (r *BackingImageReconciler) onChange(ctx context.Context, backingImage *lhv1.BackingImage) (ctrl.Result, error) {
	//logger := log.FromContext(ctx)
	if backingImage == nil || backingImage.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	if backingImage.Annotations[util.AnnotationImageID] == "" || len(backingImage.Status.DiskFileStatusMap) != 1 {
		return ctrl.Result{}, nil
	}

	namespace, name := ref.Parse(backingImage.Annotations[util.AnnotationImageID])
	vmImage := &hciv1.VirtualMachineImage{}
	err := r.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, vmImage)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !hciv1.ImageInitialized.IsTrue(vmImage) || !hciv1.ImageImported.IsUnknown(vmImage) {
		return ctrl.Result{}, nil
	}

	toUpdate := vmImage.DeepCopy()
	for _, status := range backingImage.Status.DiskFileStatusMap {
		if status.State == lhv1.BackingImageStateFailed {
			hciv1.ImageImported.False(toUpdate)
			hciv1.ImageImported.Reason(toUpdate, "ImportFailed")
			hciv1.ImageImported.Message(toUpdate, status.Message)
			toUpdate.Status.Progress = status.Progress
		} else if status.State == lhv1.BackingImageStateReady || status.State == lhv1.BackingImageStateReadyForTransfer {
			hciv1.ImageImported.True(toUpdate)
			hciv1.ImageImported.Reason(toUpdate, "Imported")
			hciv1.ImageImported.Message(toUpdate, status.Message)
			toUpdate.Status.Progress = status.Progress
			toUpdate.Status.Size = backingImage.Status.Size
		} else if status.Progress != toUpdate.Status.Progress {
			hciv1.ImageImported.Unknown(toUpdate)
			hciv1.ImageImported.Reason(toUpdate, "Importing")
			hciv1.ImageImported.Message(toUpdate, status.Message)
			// backing image file upload progress can be 100 before it is ready
			// Set VM image progress to be 99 for better UX in this case
			if status.Progress == 100 {
				toUpdate.Status.Progress = 99
			} else {
				toUpdate.Status.Progress = status.Progress
			}
		}
	}

	if !reflect.DeepEqual(vmImage, toUpdate) {
		if err := r.Update(context.TODO(), toUpdate); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

//func (r *BackingImageReconciler) onRemove(ctx context.Context, backingImage *v1beta1.BackingImage) (ctrl.Result, error) {
//	//logger := log.FromContext(ctx)
//	return ctrl.Result{}, nil
//}

// SetupWithManager sets up the controller with the Manager.
func (r *BackingImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lhv1.BackingImage{}).
		Complete(r)
}
