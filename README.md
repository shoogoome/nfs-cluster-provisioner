# Kubernetes的nfs集群动态存储卷

## 简介

在实际系统架构设计中，如果使用上了Kubernetes的动态存储卷，那么也一定是在业务上实现了分布式集群部署，
但如果仅仅使用一台nfs服务器提供数据存储的支持，那系统的分布式设计是不够彻底的。
该项目对市面上广泛传播的单机版nfs动态存储卷进行一个多nfs的适配。


## 使用说明
(ps: 文件在/deploy/storage)    

### 部署动态存储卷
部署storage-serviceaccout.yaml、storage-rbac.yaml创建账户。可根据实际情况修改命名空间等  
部署storage-deployment.yaml文件以启动动态存储卷服务, 修改环境变量和volume映射，详见下文"环境变量"配置  


### 部署StorageClass
部署storageclass.yaml  
测试：部署pvc.yaml，查看pvc是否正常创建和绑定pv

## 环境变量

| 环境变量 | 取值 |
| --- | ---- |
| PROVISIONER_NAME | 动态存储卷名 | 
| NFS_SERVER | nfs服务名，ip之间用冒号隔开。格式: 111.111.111.111:222.222.222.222|
| NFS_PATH | nfs服务路径，顺序与NFS_SERVER顺序对应，路径之间用冒号隔开 |
| LOG | 是否打印详细日志 "true" or "false" |


## 固定映射

为了在开启n台节点服务后依旧可以在nfs服务器中找到对应的位置(我们可以
设定存储路径和pv name相关，statefulset每次启动的pvname都是相同的)。但在多台nfs服务的情况下
我们需要保证每次pv卷对应的nfs服务器是固定的。这里我们采用一致性hash对pvname求得存储节点位置，
以达到复用旧数据路径的目的。

## 核心代码(golang)

### 一致性hash

使用"consistent"库实现一致性hash  
NumberOfReplicas为总节点数，包含虚拟节点和真实节点。
```
circle := consistent.New()
circle.NumberOfReplicas = 255
// 创建字符串数组，并写入nfs服务ip地址作为节点名称
nodes := make([]string, _number)
for i, n := range _serverList {
    nodes[i] = n
}
circle.Set(nodes)
```

### 创建卷

```
// 获得pvc配置
pvcNamespace := options.PVC.Namespace
pvcName := options.PVC.Name

pvName := strings.Join([]string{pvcNamespace, pvcName}, "-")
   
// 一致性hash计算得存储位置
serverName, err := p.Get(pvName)
if err != nil {
    return nil, fmt.Errorf("find position fail")
}

// 构建完整路径
fullPath := filepath.Join(mountPathBase, strings.Join([]string{serverName, pvName}, "/"))
// 路径不存在则创建路径，存在则等候直接挂载
if _, err := os.Stat(fullPath); os.IsNotExist(err) {
    println(fmt.Sprintf("创建目录: %s", fullPath))
    if err := os.MkdirAll(fullPath, 0777); err != nil {
        return nil, errors.New("unable to create directory to provision new pv: " + err.Error())
    }
    os.Chmod(fullPath, 0777)
}

// 创建pv卷
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
```

### 删除卷

```

// 获得实际存储路径
path := volume.Spec.PersistentVolumeSource.NFS.Path
server := volume.Spec.PersistentVolumeSource.NFS.Server

pvName := filepath.Base(path)
oldPath := filepath.Join(mountPathBase, strings.Join([]string{server, pvName}, "/"))

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
        cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("cp -r %s %s-archive", oldPath, oldPath))
        if err := cmd.Run(); err != nil {
            return err
        }
    }
}
// 删除时是否删除原本数据
// 填且为true则删除数据
deleteFile, exists := storageClass.Parameters["deleteFile"]
if exists {
    if deleteFile, err := strconv.ParseBool(deleteFile); err == nil && deleteFile {
        if err := os.RemoveAll(oldPath); err != nil {
            return err
        }
    }
}

```