package kraken

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"

	samsungv1alpha1 "github.com/samsung-cnct/cluster-controller/pkg/apis/clustercontroller/v1alpha1"
	clientset "github.com/samsung-cnct/cluster-controller/pkg/client/clientset/versioned"
	samsungscheme "github.com/samsung-cnct/cluster-controller/pkg/client/clientset/versioned/scheme"
	informers "github.com/samsung-cnct/cluster-controller/pkg/client/informers/externalversions"
	listers "github.com/samsung-cnct/cluster-controller/pkg/client/listers/clustercontroller/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// value in concurrent TimeMap
type ClusterCreateTimes struct {
	uuid                     string // uuid of resource
	name                     string // name of resource
	namespace                string // namespace of resource
	createStartTime          time.Time
	createBootstrapReadyTime time.Time
	createReadyTime          time.Time
	bootstrapDuration        time.Duration
	createDuration           time.Duration
	done                     bool
	bootstrapError           bool
}

var (
	// mu guards TimeMap
	mu sync.RWMutex
	// TimeMap contains ClusterCreateTimes, key is clusterName
	TimeMap map[string]ClusterCreateTimes
)

// Controller to watch KrakenClusters
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// samsungclientset is a clientset for our own api group
	samsungclientset clientset.Interface

	krakenclusterLister  listers.KrakenClusterLister
	krakenclustersSynced cache.InformerSynced
	workqueue            workqueue.RateLimitingInterface
}

func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	// don't let panics crash the process
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	glog.Info("Starting cluster-controller")

	// Wait for the caches to be synced before starting workers
	glog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.krakenclustersSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	glog.Info("Starting workers")
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	glog.Info("Started workers")
	// wait until we're told to stop
	<-stopCh
	glog.Info("Shutting down cluster-controller")

	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		//glog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}
	return true
}

func (c *Controller) processTimeMap(kc *samsungv1alpha1.KrakenCluster) {
	mu.Lock()
	if TimeMap != nil {
		times, present := TimeMap[kc.Name]
		if present {
			if kc.Status.State == "Created" && times.createReadyTime.IsZero() {
				glog.Info("Initiating Cluster Created and Ready for cluster: ", times.name)
				times.name = kc.Name
				times.uuid = string(kc.UID)
				times.createReadyTime = time.Now()
				times.createDuration = times.createReadyTime.Sub(times.createStartTime)
				times.done = true
				TimeMap[kc.Name] = times

				if c.clustersCreated() {
					TimeMap = nil
				}
			} else if kc.Status.State == "Creating" && times.createStartTime.IsZero() {
				times.createStartTime = time.Now()
				glog.Infof("Creating cluster name: %s Start time = %s", times.name, times.createStartTime.String())
				TimeMap[kc.Name] = times
			} else if kc.Status.State == "Creating" && kc.Status.Status == "JujuBootstrapReady" && times.createBootstrapReadyTime.IsZero() {
				times.createBootstrapReadyTime = time.Now()
				times.bootstrapDuration = times.createBootstrapReadyTime.Sub(times.createStartTime)
				glog.Infof("Bootstrap Ready for cluster name: %s JujuBootstrapReady time = %s", times.name, times.createBootstrapReadyTime.String())
				TimeMap[kc.Name] = times
			} else if kc.Status.State == "Creating" && kc.Status.Status == "JujuBootstrapError" {
				glog.Info("Entering boostrapError = true for cluster name: ", times.name)
				times.bootstrapError = true
				TimeMap[kc.Name] = times
				if c.clustersCreated() {
					TimeMap = nil
				}
			}
		}
	}
	mu.Unlock()
}

func (c *Controller) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the KrakenCluster resource with this namespace/name
	kc, err := c.krakenclusterLister.KrakenClusters(namespace).Get(name)
	if err != nil {
		// The KrakenCluster resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("krakencluster '%s' in work queue no longer exists", key))
			return nil
		}
		return err
	}

	// business logic
	c.processTimeMap(kc)

	return nil
}

// enqueueKrakenCluster takes a KrakenCluster resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than KrakenCluster.
func (c *Controller) enqueueKrakenCluster(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		runtime.HandleError(err)
		return
	}
	c.workqueue.AddRateLimited(key)
}

// create new controller
func NewController(
	kubeclientset kubernetes.Interface,
	samsungclientset clientset.Interface,
	samsungInformerFactory informers.SharedInformerFactory,
) *Controller {
	// obtain references to the shared index informer for KrakenCluster types
	krakenclusterInformer := samsungInformerFactory.Samsung().V1alpha1().KrakenClusters()

	// create event broadcaster so events can be logged for krakencluster types
	samsungscheme.AddToScheme(scheme.Scheme)

	c := &Controller{
		kubeclientset:        kubeclientset,
		samsungclientset:     samsungclientset,
		krakenclusterLister:  krakenclusterInformer.Lister(),
		krakenclustersSynced: krakenclusterInformer.Informer().HasSynced,
		workqueue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "KrakenClusters"),
	}

	glog.Info("Setting up event handlers")
	// Set up an event handler for when KrakenCluster resources change
	krakenclusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.enqueueKrakenCluster,
		UpdateFunc: func(old, new interface{}) {
			c.enqueueKrakenCluster(new)
		},
	})

	return c
}

func GenerateKrakenCluster(clusterName string, cloudProvider string, username string, accesskey string, password string,
	bundle string, awsRegion string, maasEndpoint string) samsungv1alpha1.KrakenCluster {
	return samsungv1alpha1.KrakenCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: "default",
		},
		Spec: samsungv1alpha1.KrakenClusterSpec{
			CustomerID: "myCustomerID",
			CloudProvider: samsungv1alpha1.CloudProviderInfo{
				Name: samsungv1alpha1.CloudProviderName(cloudProvider),
				Credentials: samsungv1alpha1.CloudProviderCredentials{
					Accesskey: accesskey,
					Password:  password,
					Username:  username,
				},
				Region: awsRegion,
			},
			Provisioner: samsungv1alpha1.ProvisionerInfo{
				Name:         "juju",
				Bundle:       bundle,
				MaasEndpoint: maasEndpoint,
			},
			Cluster: samsungv1alpha1.ClusterInfo{
				ClusterName: clusterName,
				NodePools: []samsungv1alpha1.NodeProperties{
					{
						Name:        "worker",
						PublicIPs:   false,
						Size:        1,
						MachineType: "m4.xlarge",
						Os:          "ubuntu:16:04",
					},
					{
						Name:        "master",
						PublicIPs:   false,
						Size:        1,
						MachineType: "m4.xlarge",
						Os:          "ubuntu:16:04",
					},
					{
						Name:        "etcd",
						PublicIPs:   false,
						Size:        3,
						MachineType: "m2.medium",
						Os:          "ubuntu:16:04",
					},
				},
				Fabric: samsungv1alpha1.FabricInfo{
					Name: "canal",
				},
			},
		},
	}
}

func CreateCluster(clusterName string, cloudProvider string, username string, accesskey string, password string,
	bundle string, awsRegion string, maasEndpoint string, wg *sync.WaitGroup, client clientset.Interface) error {
	defer wg.Done()
	// init ClusterCreateTimes
	glog.Info("createCluster called for cluster name = ", clusterName)
	mu.Lock()
	if TimeMap != nil {
		TimeMap[clusterName] = ClusterCreateTimes{name: clusterName, namespace: "default", done: false, bootstrapError: false}
	}
	mu.Unlock()
	// create a cluster

	cluster := GenerateKrakenCluster(clusterName, cloudProvider, username, accesskey, password,
		bundle, awsRegion, maasEndpoint)
	_, err := client.SamsungV1alpha1().KrakenClusters("default").Create(&cluster)
	return err
}

func deleteCluster(clusterName string, client clientset.Interface) error {
	glog.Info("deleteCluster called for cluster name: ", clusterName)
	err := client.SamsungV1alpha1().KrakenClusters("default").Delete(clusterName, &metav1.DeleteOptions{})
	if err != nil {
		glog.Infof("KrakenCluster -->%s<-- Cannot be Deleted, error was %v", clusterName, err)
		return err
	}
	return err
}

func (c *Controller) resultsAndCleanup(doneCount int, errorCount int) {
	var totDuration time.Duration
	var totBootstrap time.Duration
	for _, t := range TimeMap {
		if t.done {
			totDuration += t.createDuration
			totBootstrap += t.bootstrapDuration
			glog.Infof("Bootstrap Duration for uuid: %s = %s", t.uuid, t.bootstrapDuration.String())
			glog.Infof("Create Duration for uuid: %s = %s", t.uuid, t.createDuration.String())
		}

		// delete cluster
		deleteCluster(t.name, c.samsungclientset)
	}

	avgCreateNs := totDuration.Nanoseconds() / int64(doneCount)
	avgNanosecondsDuration := time.Duration(avgCreateNs)
	avgBootstrapNs := totBootstrap.Nanoseconds() / int64(doneCount)
	avgBootstrapDuration := time.Duration(avgBootstrapNs)
	glog.Info("Total Successfully Created Clusters = ", doneCount)
	glog.Info("Total JujuBootstrapError Clusters = ", errorCount)
	glog.Info("Average Successful Bootstrap Duration = ", avgBootstrapDuration.String())
	glog.Info("Average Create Duration = ", avgNanosecondsDuration.String())
}

func (c *Controller) clustersCreated() bool {
	done := false
	doneCount := 0
	errorCount := 0

	for _, t := range TimeMap {
		if t.done {
			doneCount++

		} else if t.bootstrapError {
			errorCount++
		}
		if len(TimeMap) == doneCount+errorCount {
			done = true
		}
	}
	if done {
		glog.Info("calling resultsAndCleanup")
		c.resultsAndCleanup(doneCount, errorCount)
	}

	return done
}
