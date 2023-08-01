package util

const (
	prefix                         = "hci.wjyl.com"
	RemovedPVCsAnnotationKey       = prefix + "/removedPersistentVolumeClaims"
	AdditionalCASecretName         = "hci-additional-ca"
	AdditionalCAFileName           = "additional-ca.pem"
	AnnotationMigrationTarget      = prefix + "/migrationTargetNodeName"
	AnnotationMigrationUID         = prefix + "/migrationUID"
	AnnotationMigrationState       = prefix + "/migrationState"
	AnnotationTimestamp            = prefix + "/timestamp"
	AnnotationVolumeClaimTemplates = prefix + "/volumeClaimTemplates"
	AnnotationImageID              = prefix + "/imageId"
	AnnotationHash                 = prefix + "/hash"

	BackupTargetSecretName      = "hci-backup-target-secret"
	InternalTLSSecretName       = "tls-rancher-internal"
	Rke2IngressNginxAppName     = "rke2-ingress-nginx"
	CattleSystemNamespaceName   = "cattle-system"
	LonghornSystemNamespaceName = "longhorn-system"
	KubeSystemNamespace         = "kube-system"

	AnnotationReservedMemory = prefix + "/reservedMemory"
	AnnotationRunStrategy    = prefix + "/vmRunStrategy"
	LabelImageDisplayName    = prefix + "/imageDisplayName"

	AnnotationStorageClassName          = prefix + "/storageClassName"
	AnnotationStorageProvisioner        = prefix + "/storageProvisioner"
	AnnotationIsDefaultStorageClassName = "storageclass.kubernetes.io/is-default-class"

	AnnotationDefaultUserdataSecret = prefix + "/default-userdata-secret"

	ContainerdRegistrySecretName = "hci-containerd-registry"
	ContainerdRegistryFileName   = "registries.yaml"

	LonghornDefaultManagerURL = "http://longhorn-backend.longhorn-system:9500/v1"
	FleetLocalNamespaceName   = "fleet-local"
	LocalClusterName          = "local"

	HTTPProxyEnv  = "HTTP_PROXY"
	HTTPSProxyEnv = "HTTPS_PROXY"
	NoProxyEnv    = "NO_PROXY"

	LonghornOptionBackingImageName = "backingImage"
	LonghornOptionMigratable       = "migratable"
	AddonValuesAnnotation          = "hci.wjyl.com/addon-defaults"
)
