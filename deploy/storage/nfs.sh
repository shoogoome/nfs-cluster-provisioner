#!/bin/bash


case $1 in
    "up")
        kubectl create -f storage-rbac.yaml -f storage-serviceaccount.yaml -f storage-deployment.yaml
    ;;
    "down")
        kubectl delete -f storage-deployment.yaml
    ;;
    "alldown")
        kubectl delete -f storage-rbac.yaml -f storage-serviceaccount.yaml -f storage-deployment.yaml
    ;;
    "update")
        kubectl apply -f storage-deployment.yaml
    ;;
esac

