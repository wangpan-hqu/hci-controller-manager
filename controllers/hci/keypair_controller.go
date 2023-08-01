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
	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hciv1beta1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KeyPairReconciler reconciles a Setting object
type KeyPairReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=hci.wjyl.com,resources=keypairs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=keypairs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=keypairs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Setting object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (h *KeyPairReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	// TODO(user): your logic here
	keyPair := &hciv1beta1.KeyPair{}
	if err := h.Get(ctx, req.NamespacedName, keyPair); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if keyPair == nil || keyPair.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if keyPair.Spec.PublicKey == "" || keyPair.Status.FingerPrint != "" {
		return ctrl.Result{}, nil
	}

	toUpdate := keyPair.DeepCopy()
	publicKey := []byte(keyPair.Spec.PublicKey)
	pk, _, _, _, err := ssh.ParseAuthorizedKey(publicKey)
	if err != nil {
		hciv1beta1.KeyPairValidated.False(toUpdate)
		hciv1beta1.KeyPairValidated.Reason(toUpdate, fmt.Sprintf("failed to parse the public key, error: %v", err))
	} else {
		fingerPrint := ssh.FingerprintLegacyMD5(pk)
		toUpdate.Status.FingerPrint = fingerPrint
		hciv1beta1.KeyPairValidated.True(toUpdate)
	}
	if err := h.Update(context.TODO(), toUpdate); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (h *KeyPairReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hciv1beta1.KeyPair{}).
		Complete(h)
}
