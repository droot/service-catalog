#!/bin/bash
##################################################################
#
# Author: Sean Sullivan (seans)
# Date:   08/02/2017
# Description: Helper script to bring down service catalog
#   cleanly.
#
##################################################################

NAMESPACE=service-catalog

# Delete apiserver and controller-manager deployments
kubectl delete deployment apiserver -n $NAMESPACE
kubectl delete deployment controller-manager -n $NAMESPACE

# Delete roles and bindings
kubectl delete clusterroles servicecatalog.k8s.io:apiserver
kubectl delete clusterroles servicecatalog.k8s.io:controller-manager
kubectl delete clusterrolebindings servicecatalog.k8s.io:apiserver
kubectl delete clusterrolebindings servicecatalog.k8s.io:apiserver-auth-delegator
kubectl delete clusterrolebindings servicecatalog.k8s.io:controller-manager

kubectl delete roles servicecatalog.k8s.io::leader-locking-controller-manager -n kube-system
kubectl delete rolebindings service-catalog-controller-manager -n kube-system
kubectl delete rolebindings servicecatalog.k8s.io:apiserver-authentication-reader -n kube-system

# Delete service and APIService registration
kubectl delete services service-catalog-api -n $NAMESPACE
kubectl delete apiservices v1alpha1.servicecatalog.k8s.io

# Deletes service accounts, and service
kubectl delete ns $NAMESPACE

