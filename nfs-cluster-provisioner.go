package main

import (
	"errors"
	"fmt"
	"github.com/golang/glog"
	"github.com/kubernetes-sigs/sig-storage-lib-external-provisioner/controller"
	v1 "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper"
	"os"
	"os/exec"
	"path/filepath"
	"stathat.com/c/consistent"
	"strconv"
	"strings"
	"time"
)

const (
	mountPathBase      = "/persistent-volumes" // 挂载根目录
)

type nfsClusterProvisioner struct {
	client kubernetes.Interface
	server map[string]string
	number int
	*consistent.Consistent
}

var log = false
var nfsClusterProvisionerEntity controller.Provisioner = &nfsClusterProvisioner{} // controller

// 挂载
func (p *nfsClusterProvisioner) Provision(options controller.VolumeOptions) (*v1.PersistentVolume, error) {

	println("创建卷请求")
	if options.PVC.Spec.Selector != nil {
		return nil, fmt.Errorf("claim Selector is not supported")
	}
	glog.V(4).Infof("nfs provisioner: VolumeOptions %v", options)

	pvcNamespace := options.PVC.Namespace
	pvcName := options.PVC.Name

	pvName := strings.Join([]string{pvcNamespace, pvcName}, "-")

	println(fmt.Sprintf("pvc命名空间: %s, pvc名称%s", pvcNamespace, pvcName))
	serverName, err := p.Get(pvName)
	if err != nil {
		return nil, fmt.Errorf("find position fail")
	}
	println(fmt.Sprintf("映射服务: %s", serverName))
	fullPath := filepath.Join(mountPathBase, strings.Join([]string{serverName, pvName}, "/"))

	// 没有则创建
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		println(fmt.Sprintf("创建目录: %s", fullPath))
		if err := os.MkdirAll(fullPath, 0777); err != nil {
			return nil, errors.New("unable to create directory to provision new pv: " + err.Error())
		}
		os.Chmod(fullPath, 0777)
	}

	path := filepath.Join(p.server[serverName], pvName)
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVName,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: options.PersistentVolumeReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			MountOptions:                  options.MountOptions,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{
					Server:   p.server[serverName],
					Path:     path,
					ReadOnly: false,
				},
			},
		},
	}
	println("pv卷创建成功")
	return pv, nil
}

// 删除
func (p *nfsClusterProvisioner) Delete(volume *v1.PersistentVolume) error {

	println("删除卷请求")
	path := volume.Spec.PersistentVolumeSource.NFS.Path
	server := volume.Spec.PersistentVolumeSource.NFS.Server

	pvName := filepath.Base(path)
	oldPath := filepath.Join(mountPathBase, strings.Join([]string{server, pvName}, "/"))

	println(fmt.Sprintf("删除卷地址: %s", oldPath))
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		glog.Warningf("path %s does not exist, deletion skipped", oldPath)
		return nil
	}
	// 获取storageClass
	storageClass, err := p.getClassForVolume(volume)
	if err != nil {
		return err
	}

	// 删除时是否归档一份数据
	// 填且为true则另存为一份
	archiveOnDelete, exists := storageClass.Parameters["archiveOnDelete"]
	if exists {
		if archiveBool, err := strconv.ParseBool(archiveOnDelete); err == nil && archiveBool {
			println("归档数据中....")
			cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("cp -r %s %s-archive", oldPath, oldPath))
			if err := cmd.Run(); err != nil {
				return err
			}
			println(fmt.Sprintf("数据归档成功，归档文件名: %s-archive", oldPath))
		}
	}
	// 删除时是否删除原本数据
	// 填且为true则删除数据
	deleteFile, exists := storageClass.Parameters["deleteFile"]
	if exists {
		if deleteFile, err := strconv.ParseBool(deleteFile); err == nil && deleteFile {
			println("删除原数据中...")
			if err := os.RemoveAll(oldPath); err != nil {
				return err
			}
			println(fmt.Sprintf("数据删除成功: %s", oldPath))
		}
	}
	println("删除卷请求结束")
	return nil
}

func (p *nfsClusterProvisioner) getClassForVolume(pv *v1.PersistentVolume) (*storage.StorageClass, error) {
	if p.client == nil {
		return nil, fmt.Errorf("cannot get kube client")
	}
	className := helper.GetPersistentVolumeClass(pv)
	if className == "" {
		return nil, fmt.Errorf("volume has no storage class")
	}
	class, err := p.client.StorageV1().StorageClasses().Get(className, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return class, nil
}

func println(msg ...interface{}) {
	if log {
		fmt.Println(append([]interface{}{time.Now().Format("2006-01-02 15:04:05")}, msg...)...)
	}
}

func main() {

	// 获取基本配置
	serverList := os.Getenv("NFS_SERVER")
	if serverList == "" {
		glog.Fatal("NFS_SERVER not set")
	}

	path := os.Getenv("NFS_PATH")
	if path == "" {
		glog.Fatal("NFS_PATH not set")
	}

	provisionerName := os.Getenv("PROVISIONER_NAME")
	if provisionerName == "" {
		glog.Fatal("PROVISIONER_NAME not set")
	}

	_log := os.Getenv("LOG")
	if _log, err := strconv.ParseBool(_log); err == nil && _log {
		log = true
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		glog.Fatalf("Failed to create config: %v", err)
	}
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}

	serverVersion, err := clientSet.Discovery().ServerVersion()
	if err != nil {
		glog.Fatalf("Error getting server version: %v", err)
	}

	// 写入nfs服务
	_serverList := strings.Split(serverList, ":")
	_path := strings.Split(path, ":")
	_number := len(_serverList)

	server := make(map[string]string)
	for i := 0; i < _number; i++ {
		server[_serverList[i]] = _path[i]
	}

	// 创建一致性哈希
	circle := consistent.New()
	circle.NumberOfReplicas = 255
	nodes := make([]string, _number)
	for i, n := range _serverList {
		nodes[i] = n
	}
	circle.Set(nodes)

	nfsClusterProvisionerEntity = &nfsClusterProvisioner{
		client:     clientSet,
		server:     server,
		number:     _number,
		Consistent: circle,
	}

	pc := controller.NewProvisionController(clientSet, provisionerName, nfsClusterProvisionerEntity, serverVersion.GitVersion)
	pc.Run(wait.NeverStop)
}
