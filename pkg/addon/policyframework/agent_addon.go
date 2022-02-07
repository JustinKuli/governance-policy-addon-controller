package policyframework

import (
	"context"
	"embed"
	"strings"

	"github.com/openshift/library-go/pkg/assets"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonapiv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

var (
	genericScheme = runtime.NewScheme()
	genericCodecs = serializer.NewCodecFactory(genericScheme)
	genericCodec  = genericCodecs.UniversalDeserializer()
)

const (
	// addOnAgentInstallationNamespace is the namespace on the managed cluster to install the helloworldhelm addon agent.
	addOnAgentInstallationNamespace = "open-cluster-management-agent-addon"
	defaultExampleImage             = "quay.io/open-cluster-management/helloworld-addon:latest"
	addonName                       = "governance-policy-framework"
)

func init() {
	scheme.AddToScheme(genericScheme)
}

//go:embed manifests
//go:embed manifests/managedclusterchart
//go:embed manifests/managedclusterchart/templates/_helpers.tpl
var FS embed.FS

var agentPermissionFiles = []string{
	// role with RBAC rules to access resources on hub
	"manifests/hubpermissions/role.yaml",
	// rolebinding to bind the above role to a certain user group
	"manifests/hubpermissions/rolebinding.yaml",
}

func newRegistrationOption(kubeConfig *rest.Config, recorder events.Recorder, agentName string) *agent.RegistrationOption {
	return &agent.RegistrationOption{
		CSRConfigurations: agent.KubeClientSignerConfigurations(addonName, agentName),
		CSRApproveCheck:   utils.DefaultCSRApprover(agentName),
		PermissionConfig: func(cluster *clusterv1.ManagedCluster, addon *addonapiv1alpha1.ManagedClusterAddOn) error {
			kubeclient, err := kubernetes.NewForConfig(kubeConfig)
			if err != nil {
				return err
			}

			for _, file := range agentPermissionFiles {
				if err := applyManifestFromFile(file, cluster.Name, addon.Name, kubeclient, recorder); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

func applyManifestFromFile(file, clusterName, addonName string, kubeclient *kubernetes.Clientset, recorder events.Recorder) error {
	groups := agent.DefaultGroups(clusterName, addonName)
	config := struct {
		ClusterName string
		Group       string
	}{
		ClusterName: clusterName,
		Group:       groups[0],
	}

	results := resourceapply.ApplyDirectly(context.Background(),
		resourceapply.NewKubeClientHolder(kubeclient),
		recorder,
		resourceapply.NewResourceCache(),
		func(name string) ([]byte, error) {
			template, err := FS.ReadFile(file)
			if err != nil {
				return nil, err
			}
			return assets.MustCreateAssetFromTemplate(name, template, config).Data, nil
		},
		file,
	)

	for _, result := range results {
		if result.Error != nil {
			return result.Error
		}
	}

	return nil
}

type global struct {
	ImagePullPolicy string            `json:"imagePullPolicy"`
	ImagePullSecret string            `json:"imagePullSecret"`
	ImageOverrides  map[string]string `json:"imageOverrides"`
	NodeSelector    map[string]string `json:"nodeSelector"`
	ProxyConfig     map[string]string `json:"proxyConfig"`
}

type userValues struct {
	ClusterNamespace  string `json:"clusterNamespace"`
	LogLevel          int32  `json:"logLevel"`
	Global            global `json:"global"`
	OnMulticlusterHub bool   `json:"onMulticlusterHub"`
}

func getValues(cluster *clusterv1.ManagedCluster,
	addon *addonapiv1alpha1.ManagedClusterAddOn) (addonfactory.Values, error) {
	userValues := userValues{
		ClusterNamespace: cluster.GetName(),
		Global: global{
			ImagePullPolicy: "IfNotPresent",
			ImageOverrides: map[string]string{
				"helloWorldHelm": defaultExampleImage,
			},
			NodeSelector: map[string]string{},
			ProxyConfig: map[string]string{
				"HTTP_PROXY":  "",
				"HTTPS_PROXY": "",
				"NO_PROXY":    "",
			},
		},
		OnMulticlusterHub: false,
	}
	// special case for local-cluster
	if cluster.Name == "local-cluster" {
		userValues.OnMulticlusterHub = true
	}
	if val, ok := addon.GetAnnotations()["addon.open-cluster-management.io/on-multicluster-hub"]; ok {
		if strings.EqualFold(val, "true") {
			userValues.OnMulticlusterHub = true
		} else if strings.EqualFold(val, "false") {
			// the special case can still be overridden by this annotation
			userValues.OnMulticlusterHub = false
		} else {
			// TODO: should this log or return an error? The annotation should be true or false
		}
	}
	return addonfactory.JsonStructToValues(userValues)
}

func GetAgentAddon(controllerContext *controllercmd.ControllerContext) (agent.AgentAddon, error) {
	registrationOption := newRegistrationOption(
		controllerContext.KubeConfig,
		controllerContext.EventRecorder,
		addonName)

	return addonfactory.NewAgentAddonFactory(addonName, FS, "manifests/managedclusterchart").
		WithGetValuesFuncs(getValues, addonfactory.GetValuesFromAddonAnnotation).
		WithAgentRegistrationOption(registrationOption).
		BuildHelmAgentAddon()
}