# krakencluster-perf

This tool is used to create a specified number of krakenclusters, time the bootstrap and total creation time, and then delete the clusters.  This tests the performance of multiple clusters created at one time.
The cluster-controller which provides the CRD for krakenclusters must be running so that this tool can time creating the krakenclusters.  See: https://github.com/samsung-cnct/cluster-controller

## To build
    cd $GOPATH/src/github.com
    mkdir oneilcin
    cd oneilcin
    git clone https://github.com/oneilcin/krakencluster-perf
    cd krakencluster-perf
    dep ensure -v
    go build
    go install

## Usage
    go run main.go --help


Example usage for AWS:

    go run main.go --kubeconfig=$HOME/.kube/config --numberClusters=1 --username=myusername --cloudProvider=aws --accesskey=myawsaccesskeyid --password=myawssecretkey --logtostderr=true
