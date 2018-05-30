package main

import (
	"flag"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/satori/go.uuid"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	krakenListener "github.com/oneilcin/krakencluster-perf/pkg/controller/kraken"

	clientset "github.com/samsung-cnct/cluster-controller/pkg/client/clientset/versioned"
	informers "github.com/samsung-cnct/cluster-controller/pkg/client/informers/externalversions"
	"github.com/samsung-cnct/cluster-controller/pkg/signals"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var masterURL string

func viperInit() {
	// using standard library "flag" package
	flag.Int("numberClusters", 1, "number of krakenclusters to create")
	flag.String("kubeconfig", "", "path to a kube config. Only required if out-of-cluster.")
	flag.String("username", "", "username for maas or aws")
	flag.String("accesskey", "", "aws accesskey id")
	flag.String("password", "", "secret password for maas or aws")
	flag.String("cloudProvider", "aws", "aws, or maas, defaults to aws")
	flag.String("bundle", "cs:bundle/kubernetes-core-306", "juju bundle name")
	flag.String("awsRegion", "aws/us-west-2", "aws region")
	flag.String("maasEndpoint", "http://192.168.2.24/MAAS/api/2.0", "maas api endpoint")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
}

// return rest config, if path not specified assume in cluster config
func getClientConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	}
	return rest.InClusterConfig()
}

func main() {
	viperInit()

	// Create the client config. Use kubeconfig if given, otherwise assume in-cluster.
	kubeconf := viper.GetString("kubeconfig")
	config, err := getClientConfig(kubeconf)
	if err != nil {
		glog.Fatal("Error loading cluster config: %s", err.Error())
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	samsungClient, err := clientset.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Error building example clientset: %s", err.Error())
	}

	glog.Info("Constructing informer factory")
	samsungInformerFactory := informers.NewSharedInformerFactory(samsungClient, time.Second*30)

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	glog.Info("Constructing controller")
	controller := krakenListener.NewController(kubeClient, samsungClient, samsungInformerFactory)
	go samsungInformerFactory.Start(stopCh)

	numClusters := viper.GetInt("numberClusters")
	username := viper.GetString("username")
	accesskey := viper.GetString("accesskey")
	password := viper.GetString("password")
	cloudProvider := viper.GetString("cloudProvider")
	bundle := viper.GetString("bundle")
	awsRegion := viper.GetString("awsRegion")
	maasEndpoint := viper.GetString("maasEndpoint")
	var wg sync.WaitGroup
	krakenListener.TimeMap = make(map[string]krakenListener.ClusterCreateTimes)
	wg.Add(numClusters)
	for i := 0; i < numClusters; i++ {
		id := uuid.NewV4()
		go krakenListener.CreateCluster(id.String(), cloudProvider, username, accesskey, password,
			bundle, awsRegion, maasEndpoint, &wg, samsungClient)
	}
	wg.Wait()
	glog.Info("back from creating clusters")

	if err = controller.Run(1, stopCh); err != nil {
		glog.Fatalf("Error running controller: %s", err.Error())
	}
}
