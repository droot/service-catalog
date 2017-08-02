#!/bin/bash
##################################################################
#
# Author: Sean Sullivan (seans)
# Date:   08/02/2017
# Description: Helper script to bring up service catalog using
# the YAML files in this directory. The flow is basically:
#   1) Create namespace
#   2) Create service account, roles, and bindings
#   3) Create service and register
#   4) Deploy apiserver and controller manager.
# After this script is run, the user should be able to issue
# the command: kubectl api-versions
# and see "servicecatalog.k8s.io/v1alpha1" listed.
#
##################################################################

# 1) Create namespace for service catalog resources
kubectl create -f ./namespace.yaml

# 2) Create service accounts, roles, and bindings
kubectl create -f ./service-accounts.yaml
kubectl create -f ./roles.yaml

# 3) Create service and register
kubectl create -f ./service.yaml
kubectl create -f ./api-registration.yaml

# 4) Deploy apiserver and controller manager
kubectl create -f ./secret.yaml
kubectl create -f ./apiserver-deployment.yaml
kubectl create -f ./controller-manager-deployment.yaml
