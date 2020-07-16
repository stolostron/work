package spoke

import (
	"context"
	"time"

	"github.com/open-cluster-management/work/pkg/helper"
	"github.com/open-cluster-management/work/pkg/spoke/controllers/appliedmanifestcontroller"
	"github.com/open-cluster-management/work/pkg/spoke/controllers/finalizercontroller"
	"github.com/open-cluster-management/work/pkg/spoke/controllers/manifestcontroller"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/discovery"
	cacheddiscovery "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	workclientset "github.com/open-cluster-management/api/client/work/clientset/versioned"
	workinformers "github.com/open-cluster-management/api/client/work/informers/externalversions"
	"github.com/open-cluster-management/work/pkg/spoke/resource"
)

// WorkloadAgentOptions defines the flags for workload agent
type WorkloadAgentOptions struct {
	HubKubeconfigFile string
	SpokeClusterName  string
}

// NewWorkloadAgentOptions returns the flags with default value set
func NewWorkloadAgentOptions() *WorkloadAgentOptions {
	return &WorkloadAgentOptions{}
}

// AddFlags register and binds the default flags
func (o *WorkloadAgentOptions) AddFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	// This command only supports reading from config
	flags.StringVar(&o.HubKubeconfigFile, "hub-kubeconfig", o.HubKubeconfigFile, "Location of kubeconfig file to connect to hub cluster.")
	flags.StringVar(&o.SpokeClusterName, "spoke-cluster-name", o.SpokeClusterName, "Name of spoke cluster.")
}

// RunWorkloadAgent starts the controllers on agent to process work from hub.
func (o *WorkloadAgentOptions) RunWorkloadAgent(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	// build hub client and informer
	hubRestConfig, err := clientcmd.BuildConfigFromFlags("" /* leave masterurl as empty */, o.HubKubeconfigFile)
	if err != nil {
		return err
	}
	hubhash := helper.HubHash(hubRestConfig.Host)

	hubWorkClient, err := workclientset.NewForConfig(hubRestConfig)
	if err != nil {
		return err
	}
	// Only watch the cluster namespace on hub
	workInformerFactory := workinformers.NewSharedInformerFactoryWithOptions(hubWorkClient, 5*time.Minute, workinformers.WithNamespace(o.SpokeClusterName))

	// Build dynamic client and informer for spoke cluster
	spokeRestConfig := controllerContext.KubeConfig
	spokeDynamicClient, err := dynamic.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}
	spokeKubeClient, err := kubernetes.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}
	spokeAPIExtensionClient, err := apiextensionsclient.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}
	spokeWorkClient, err := workclientset.NewForConfig(spokeRestConfig)
	if err != nil {
		return err
	}
	spokeWorkInformerFactory := workinformers.NewSharedInformerFactory(spokeWorkClient, 5*time.Minute)
	// Start restmapper gorountine that refresh cached APIGroupResources in the memory
	// using discovery client
	spokeDiscoveryClient, err := discovery.NewDiscoveryClientForConfig(spokeRestConfig)
	if err != nil {
		return err
	}
	cachedSpokeDiscoveryClient := cacheddiscovery.NewMemCacheClient(spokeDiscoveryClient)
	restMapper := resource.NewMapper(cachedSpokeDiscoveryClient)
	go restMapper.Run(ctx.Done())

	manifestWorkController := manifestcontroller.NewManifestWorkController(
		ctx,
		controllerContext.EventRecorder,
		spokeDynamicClient,
		spokeKubeClient,
		spokeAPIExtensionClient,
		hubWorkClient.WorkV1().ManifestWorks(o.SpokeClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.SpokeClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash,
		restMapper,
	)
	addFinalizerController := finalizercontroller.NewAddFinalizerController(
		controllerContext.EventRecorder,
		hubWorkClient.WorkV1().ManifestWorks(o.SpokeClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.SpokeClusterName),
	)
	appliedManifestWorkFinalizeController := finalizercontroller.NewAppliedManifestWorkFinalizeController(
		controllerContext.EventRecorder,
		spokeDynamicClient,
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
	)
	manifestWorkFinalizeController := finalizercontroller.NewManifestWorkFinalizeController(
		controllerContext.EventRecorder,
		hubWorkClient.WorkV1().ManifestWorks(o.SpokeClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.SpokeClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash,
	)
	appliedManifestWorkController := appliedmanifestcontroller.NewAppliedManifestWorkController(
		controllerContext.EventRecorder,
		spokeDynamicClient,
		hubWorkClient.WorkV1().ManifestWorks(o.SpokeClusterName),
		workInformerFactory.Work().V1().ManifestWorks(),
		workInformerFactory.Work().V1().ManifestWorks().Lister().ManifestWorks(o.SpokeClusterName),
		spokeWorkClient.WorkV1().AppliedManifestWorks(),
		spokeWorkInformerFactory.Work().V1().AppliedManifestWorks(),
		hubhash,
	)

	go workInformerFactory.Start(ctx.Done())
	go spokeWorkInformerFactory.Start(ctx.Done())
	go addFinalizerController.Run(ctx, 1)
	go appliedManifestWorkFinalizeController.Run(ctx, 1)
	go appliedManifestWorkController.Run(ctx, 1)
	go manifestWorkController.Run(ctx, 1)
	go manifestWorkFinalizeController.Run(ctx, 1)
	<-ctx.Done()
	return nil
}
