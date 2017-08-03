# GCE/GKE Service Catalog Demo

[cfssl]: https://github.com/cloudflare/cfssl
[Create a new GCP project]: https://cloud.google.com/resource-manager/docs/creating-managing-projects
[helm]: https://docs.helm.sh/
[service account]: https://cloud.google.com/compute/docs/access/service-accounts
[service catalog developer guide]: https://github.com/kubernetes-incubator/service-catalog/blob/master/docs/devguide.md
[service catalog repo]: https://github.com/kubernetes-incubator/service-catalog
[service catalog walkthrough]: https://github.com/kubernetes-incubator/service-catalog/blob/master/docs/walkthrough.md
[gcloud sdk]: https://cloud.google.com/sdk/downloads
[billable]: https://support.google.com/cloud/answer/6158867?hl=en
[docker]: https://docs.docker.com/engine/installation/linux/docker-ce/ubuntu/
[no-sudo-docker]: https://docs.docker.com/engine/installation/linux/linux-postinstall/#manage-docker-as-a-non-root-user
[Googler billing]: https://g3doc.corp.google.com/cloud/kubernetes/g3doc/dev/dev_project_setup.md#2-setup-billing
[GCP pubsub]: https://cloud.google.com/pubsub/

Tutorial for launching Kubernetes with a _Service Catalog_ on the
_Google Compute Platform_ (GCP) talking to the _GCP Service Broker_.
This is intended to cover launching on Google Compute Engine (GCE)
and on the Google Container Engine (GKE).
Adapted from the [service catalog walkthrough].

 * [Preconditions](#preconditions)
 * [Crucial environment variables](#crucial-environment-variables)
 * [Preliminaries](#preliminaries)
    * [Login](#login)
    * [Create a project](#create-a-project)
    * [Start a Cluster](#start-a-cluster)
       * [Install and Run Kubernetes](#install-and-run-kubernetes)
       * [Install helm](#install-helm)
    * [Build and Upload Service Catalog Binaries](#build-and-upload-service-catalog-binaries)
 * [Enable the Service Broker Access API](#enable-the-service-broker-access-api)
 * [Define Process Identities](#define-process-identities)
    * [Create a CGP Service Account](#create-a-cgp-service-account)
    * [Generate Service Catalog to Broker keys](#generate-service-catalog-to-broker-keys)
 * [Start the Service Catalog in k8s](#start-the-service-catalog-in-k8s)
    * [Load a secret key for the Service Account](#load-a-secret-key-for-the-service-account)
    * [Create the broker in k8s](#create-the-broker-in-k8s)
    * [Provision a broker instance](#provision-a-broker-instance)
    * [Create the binding instance](#create-the-binding-instance)
 * [TODO insert real pubsub example](#todo-insert-real-pubsub-example)


## Preconditions

* Have the [gcloud sdk] installed; you should be able to enter
  `gcloud version`.
* Install [docker]; ideally you can run `docker run hello-world`
  [without resorting to `sudo`][no-sudo-docker].
* [Create a new GCP project] to avoid config collisions with
  existing projects. Make sure the project is [billable].
* Use a `@google.com` account for authentication, since the GCP
  broker API below is only enabled for Google accounts.
  Specifically:
  * `gcloud auth` must be done in context of that Google account.
  * Also, the GCP project must list said account as one of its owners.

## Crucial environment variables

Set these as desired:

```
# Specify a Google account that owns the project
export TUT_OWNER=<someuser>@google.com

# Specify the case-sensitive Project ID, either
# an existing ID or a new one.
export TUT_PROJECT_ID=<some-project-name, e.g. binary-pumpkin-toast>

# Workspace used by this tutorial to create files.
TUT_DIR=$(mktemp -d)
```

Optional, for visiting link that take shell vars as args:
```
# Handy to open URLs from command line.
BROWSER=/opt/google/chrome/chrome  # chromium-browser

# The GCP developer console
CONSOLE_URL=https://console.cloud.google.com
```

## Preliminaries

This section covers things a user would likely already have
done - e.g. choose a project, install gcloud, start a k8s
cluster, compile the service-catalog, etc.  This may inform an
integration test later.  Helm installation included for now but
may go away soon.

This section takes around 5 minutes to perform, while the rest of
the (key generation, kubectl resource instantiation, etc.) takes
well under a minute.

### Login

```
gcloud config set account $TUT_OWNER
gcloud auth login
```

### Create a project

If not already created, do so:
```
gcloud projects create $TUT_PROJECT_ID
```

Point to it.
```
gcloud config set project $TUT_PROJECT_ID
gcloud config list
```

Verify `billingEnabled: true` for your project
(see [how to enable billing][billable]; Googlers
see [Googler billing]).

```
gcloud alpha billing accounts list
gcloud alpha billing accounts projects describe $TUT_PROJECT_ID
```

### Start a Cluster

#### Install and Run Kubernetes

Before bringing up a cluster as a Googler, one
must enable these alpha features.

```
gcloud alpha service-management enable container --project $TUT_PROJECT_ID
gcloud alpha service-management enable test-container.sandbox.googleapis.com --project $TUT_PROJECT_ID
gcloud alpha service-management enable compute-component.googleapis.com --project $TUT_PROJECT_ID
```

Bring up a Google Computer Engine (GCE) cluster
using the latest official _k8s_ release.

```
cd $TUT_DIR
curl -sS https://get.k8s.io | bash
PATH=$TUT_DIR/kubernetes/client/bin:$PATH
$TUT_DIR/kubernetes/cluster/kube-up.sh
```

This should conclude with something like:

> ```
> Kubernetes master is running at FOO
> GLBCDefaultBackend is running at FOO/...
> Heapster is running at FOO/api/v1/...
> KubeDNS is running at FOO/api/v1/...
> ```

etc.

#### Install helm

Install and initialize [helm], because (for now) some setup steps
below take the form of helm charts (templates describing
kubernetes resources).

```
cd $TUT_DIR
curl https://storage.googleapis.com/kubernetes-helm/helm-v2.5.1-linux-amd64.tar.gz | tar zxvf -
PATH=$TUT_DIR/linux-amd64:$PATH
helm init; sleep 8
helm version
```

This should end by reporting your helm server and client version.

### Build and Upload Service Catalog Binaries

Clone the [service catalog repo] and grab patches needed to build
the service catalog binaries.

```
cd $TUT_DIR
git clone git@github.com:kubernetes-incubator/service-catalog.git
cd service-catalog

git remote add richardfung \
    https://github.com/richardfung/service-catalog.git
git fetch richardfung
git checkout -b gcp richardfung/google
```

Define the `REGISTRY` environment variable for the benefit of the
makefile (see the [service catalog developer guide]), and build
the necessary images:

```
# *** Don't forget trailing slash - make won't work without. ***
export REGISTRY=gcr.io/${TUT_PROJECT_ID}/

# Clean first if TUT_PROJECT_ID has changed since last build.
# Some of the binaries created by
# docker in the build end up owned by root, requiring a chown.
cd $TUT_DIR/service-catalog
sudo chown -R $USER ./
make clean

cd $TUT_DIR/service-catalog
make build
```

To upload images, your project must have enabled the _Google
Container Registry API_ for billing.

There are lots of APIs:

```
gcloud service-management list --available --sort-by="NAME"
```

Enable the the (staged) Container Registry API, and the Compute Engine API:

```
gcloud service-management enable containerregistry.googleapis.com
```

Confirm

```
gcloud service-management list --enabled --filter='NAME:containerregistry*'
```

Before uploading, review your current list of images:

```
gcloud alpha container images list --repository=gcr.io/$TUT_PROJECT_ID
```

Upload your newly built images for apiserver, controller, etc.

```
# Authenticate with the docker runtime
gcloud docker -a

# Upload.
make images push
```

Verify the upload

```
gcloud alpha container images list --repository=gcr.io/$TUT_PROJECT_ID
```

Expect these:
> ```
> gcr.io/svc-cat-tangle/apiserver
> gcr.io/svc-cat-tangle/controller-manager
> gcr.io/svc-cat-tangle/user-broker
> ```

## Enable the Service Broker Access API

If not already enabled, enable the _staging_ versions of the
Broker and Registry API.

`TUT_OWNER` must be a Googler for this to work.

> Hyphen trouble. We have both 
> ```
>  1) staging-servicebroker.sandbox.googleapis.com
>  2) staging-service-broker.sandbox.googleapis.com
> ```
> and likewise for `serviceregistry`.  (1) seems to work,
> while (2) seems to not work.

```
export TUT_BROKER_API_HOST=staging-servicebroker.sandbox.googleapis.com
gcloud service-management enable $TUT_BROKER_API_HOST
gcloud service-management enable staging-servicebroker.sandbox.googleapis.com
gcloud service-management enable staging-serviceregistry.sandbox.googleapis.com
```

If you see

> ```
> ERROR: (gcloud.service-management.enable) You do not have permission to
> access service [staging-serviceregistry.sandbox.googleapis.com:enable]
> ```

then this API is not available to you, and the most likely reason
is that you've not authenticated with a `@google.com` account -
see [login](#login).

Confirm that the apis are available:

```
gcloud service-management list --available | grep staging | egrep '(broker|registry)'
```

## Define Process Identities

### Create a CGP Service Account

Any process in GCE must be associated with a [service account] to
authenticate itself to GCP services.

Useful commands to see the service accounts and members (human accounts)
currently associated with the project:

```
# Likely this will list zero items:
gcloud iam service-accounts list

# Review current role assignments:
gcloud projects get-iam-policy $TUT_PROJECT_ID 
```

Make a GCP service account, associated with this project, dedicated
for exclusive use by the service catalog process.

 * It's desirable to modify GCP access levels (roles) associated
   with the service catalog account without impacting other
   accounts.

 * Billing for the project is broken out by service accounts,
   so one can see the impact of the service catalog on billing.

Use a self-documenting account name (easier to interpret in
`gcloud` command output):

```
export TUT_SVC_ACCT=service-catalog-gcp
```

Create it, documenting it with a useful display name.

```
gcloud beta iam service-accounts create $TUT_SVC_ACCT \
    --display-name "Service Catalog Account"
```

Verify account creation:

```
export TUT_FULL_SVC_ACCT=${TUT_SVC_ACCT}@${TUT_PROJECT_ID}.iam.gserviceaccount.com

gcloud beta iam service-accounts describe $TUT_FULL_SVC_ACCT
```

Let the service account be an _editor_ (TODO: explain why):

```
gcloud projects add-iam-policy-binding $TUT_PROJECT_ID \
  --member serviceAccount:${TUT_FULL_SVC_ACCT} \
  --role roles/editor
```

Verify this role change:
```
gcloud projects get-iam-policy $TUT_PROJECT_ID
```

If desired, confirm via the web at
```
$BROWSER $CONSOLE_URL/iam-admin/serviceaccounts/project?project=$TUT_PROJECT_ID
```

> Now we hope that the service account associated with the GCP
> service broker process also had the editor role for TBD reasons.
> These commands need to be run by the broker admin (e.g. a GKE SRE).
>
> ```
> # gcloud projects get-iam-policy <plori-gcp-demo> account.json
> # gcloud projects set-iam-policy <plori-gcp-demo> account.json
> ```
>
> TODO: Maybe(?) later we'll define a _kubernetes_ service account that
>       serves a similar purpose (process authentication and
>       authorization) with kubernetes.


### Generate Service Catalog to Broker keys

The service-catalog process in GCE, which will run with the `$TUT_SVC_ACCT`
identity, needs keys to authenticate with the GCP Broker service.

Make the CloudFare [cfssl] tool available for key manipulation:

```
GOPATH=$TUT_DIR
go get -u github.com/cloudflare/cfssl/cmd/...
```

Make the keys

```
# These are used later to locate key files.
export TUT_BARE_CA=ca
export TUT_BARE_API=apiserver

function TUT_confirmFile {
  if [ -f "$1" ]; then
    echo Got $1
  else
    echo Problem creating $1
  fi
}

function TUT_makeKeys {
  local bin=$TUT_DIR/bin

  local helmName=catalog
  local svcCatNamespace=catalog
  local svcCatServiceName=${helmName}-${svcCatNamespace}-apiserver

  # TODO: Document these host choices.
  local h1=${svcCatServiceName}.${svcCatNamespace}
  local h2=${svcCatServiceName}.${svcCatNamespace}.svc
  local hosts=\"$h1\",\"$h2\"

  # Generate a self-signed root certificate request bundle,
  # and have cfssljson split the bundle into the files
  # ca.csr, ca.pem, and ca-key.pem.

  cat <<EOF | \
      $bin/cfssl genkey --initca - | \
      $bin/cfssljson -bare $TUT_BARE_CA
  {
    "hosts": [ ${hosts} ],
    "key": {
        "algo": "rsa",
        "size": 2048
    },
    "names": [
        {
            "C": "US",
            "L": "san jose",
            "O": "kube",
            "OU": "WWW",
            "ST": "California"
        }
    ]
  }
EOF

  # Configure the cert generation process.
  local configFile=$(mktemp --tmpdir=$TUT_DIR)
  cat > $configFile <<EOF
  {
    "signing": { 
      "default": {
        "expiry": "43800h",
        "usages": [ "signing", "key encipherment", "server" ]
      }
    }
  }
EOF

  # FWIW, one can compare the config we'll use to the defaults:
  $bin/cfssl print-defaults config
  cat $configFile

  # Now using this self-signed certificate authority info,
  # generates a locally issued cert and key:
  cat <<EOF | \
      $bin/cfssl gencert \
          -ca=${TUT_BARE_CA}.pem \
          -ca-key=${TUT_BARE_CA}-key.pem \
          -config=$configFile - | \
      $bin/cfssljson -bare ${TUT_BARE_API}
  {
    "CN": "${svcCatServiceName}",
    "hosts": [ ${hosts} ],
    "key": {
      "algo": "rsa",
      "size": 2048
    }
  }
EOF

  echo "PKI/TLS files for use with the GCP service account:"
  TUT_confirmFile ${TUT_BARE_CA}.pem
  TUT_confirmFile ${TUT_BARE_API}.pem
  TUT_confirmFile ${TUT_BARE_API}-key.pem
}

cd $TUT_DIR
TUT_makeKeys
```		

## Start the Service Catalog in k8s

Start the service, injecting the keys created in the previous step.

```
function TUT_installChart {

  cd $TUT_DIR/service-catalog

  # First add some priority settings to the file.
  local file=charts/catalog/templates/apiregistration.yaml
  local newTxt="\\ groupPriorityMinimum: 2000\\n\\ \\ versionPriority: 10"
  sed -i "/priority: 100/a\ ${newTxt}" $file

  helm install charts/catalog \
      --name catalog \
      --namespace catalog \
      --set useAggregator=true \
      --set apiserver.auth.enabled=true \
      --set apiserver.tls.ca=$(base64 --wrap 0 $TUT_DIR/${TUT_BARE_CA}.pem) \
      --set apiserver.tls.cert=$(base64 --wrap 0 $TUT_DIR/${TUT_BARE_API}.pem) \
      --set apiserver.tls.key=$(base64 --wrap 0 $TUT_DIR/${TUT_BARE_API}-key.pem) \
      --set apiserver.storage.type=etcd \
      --set apiserver.image=gcr.io/$TUT_PROJECT_ID/apiserver:canary \
      --set controllerManager.image=gcr.io/$TUT_PROJECT_ID/controller-manager:canary
}

TUT_installChart
```

Sanity checks
```
helm ls
kubectl get ns 

# Redo until the two "catalog" pods are READY
kubectl get pods -n catalog

# expect No resources found (i.e. it knows what a broker is)
kubectl get brokers

# confirm that kubernetes has a service catalog API
kubectl api-versions | grep servicecatalog
```


### Load a secret key for the Service Account

```
export TUT_SVC_SECRET=service-account-secret

function TUT_createServiceAccountKey {
  local tmpFile=$(mktemp --tmpdir=$TUT_DIR)
  gcloud beta iam service-accounts keys create \
      --iam-account $TUT_FULL_SVC_ACCT $tmpFile

  local jsonWebToken=$(base64 --wrap 0 $tmpFile)
  
  # Don't know why this is encoded.
  local scopes=$(echo \[\"https://www.googleapis.com/auth/cloud-platform\"\] \
     | base64 --wrap 0 - )

  cat <<EOF | kubectl create -f -
apiVersion: v1
kind: Secret
metadata:
  name: $TUT_SVC_SECRET
type: Opaque
data:
  jwt: $jsonWebToken
  scopes: $scopes
EOF
}

TUT_createServiceAccountKey

# Expect something here:
kubectl get secrets | grep ${TUT_SVC_SECRET}
```


### Create the broker in k8s

```
export TUT_BROKER_NAME=gcp-staging-broker

cat <<EOF | kubectl create -f -
  apiVersion: servicecatalog.k8s.io/v1alpha1
  kind: Broker
  metadata:
    name: $TUT_BROKER_NAME
  spec:
    url: https://${TUT_BROKER_API_HOST}/v1alpha1/projects/plori-dm-demo
    authInfo:
      oAuthSecret:
        apiVersion: v1
        name: $TUT_SVC_SECRET
        namespace: default
        kind: Secret
EOF

kubectl get serviceclasses
kubectl get brokers $TUT_BROKER_NAME -o yaml -n catalog
```


### Provision a broker instance

We'll attempt to use [GCP pubsub] for the demo.

> ```
> TODO: This is a work in progress - need more details
>       to build a pubsub demo.  Not sure about "topic",
>       "subscription", etc.
> ```

```
# Instances and bindings live in their own namespace.
export TUT_BINDING_NAMESPACE=binding-namespace
kubectl create ns $TUT_BINDING_NAMESPACE

export TUT_BROKER_INSTANCE=gcp-pubsub-broker-instance

pubsubTopic=my-topic
pubsubSubscription=my-subscription

# Create the pubsub broker resource
cat <<EOF | kubectl create -f -
  apiVersion: servicecatalog.k8s.io/v1alpha1
  kind: Instance
  metadata:
    name: $TUT_BROKER_INSTANCE
    namespace: $TUT_BINDING_NAMESPACE
  spec:
    serviceClassName: google-pubsub
    planName: default
    parameters:
      resources:
      - name: $pubsubTopic
        type: pubsub.v1.topic
        properties:
          topic: my-super-topic
      - name: $pubsubSubscription
        type: pubsub.v1.subscription
        properties:
          subscription: $pubsubSubscription
          topic: "\$(ref.my-topic.name)"
EOF

# Sanity check
kubectl get instances -n $TUT_BINDING_NAMESPACE \
    $TUT_BROKER_INSTANCE -o yaml | grep Successfully

```

### Create the binding instance

`TUT_POD_SECRET` is the ultimate secret we want to inject into the pods
so that they can use the service discovered via the service catalog.


```
TUT_POD_SECRET=binding-secret

cat <<EOF | kubectl create -f -
  apiVersion: servicecatalog.k8s.io/v1alpha1
  kind: Binding
  metadata:
    name: gcp-pubsub-broker-binding-instance
    namespace: $TUT_BINDING_NAMESPACE
  spec:
    instanceRef:
      name: $TUT_BROKER_INSTANCE
    secretName: $TUT_POD_SECRET
EOF

# Do a fully qualified group.kind.version because of a namespace
# bug currently in service catalog.
kubectl get binding.servicecatalog.k8s.io -n $TUT_BINDING_NAMESPACE -o yaml

kubectl get secrets $TUT_POD_SECRET -n $TUT_BINDING_NAMESPACE -o yaml

```

## TODO insert real pubsub example

I.e. run a k8s app that exploits GCP pubsub via these broker bindings.


