# etcd_file_syncer

## How to test?

1. You need to have local etcd server running
   ```
   export NODE1=192.168.1.21
   docker volume create --name etcd-data
   export DATA_DIR="etcd-data"
   REGISTRY=quay.io/coreos/etcd

   docker run \
     -p 2379:2379 \
     -p 2380:2380 \
     --volume=${DATA_DIR}:/etcd-data \
     --name etcd ${REGISTRY}:latest \
     /usr/local/bin/etcd \
     --data-dir=/etcd-data --name node1 \
     --initial-advertise-peer-urls http://${NODE1}:2380 --listen-peer-urls http://0.0.0.0:2380 \
     --advertise-client-urls http://${NODE1}:2379 --listen-client-urls http://0.0.0.0:2379 \
     --initial-cluster node1=http://${NODE1}:2380
   ```
   [Run etcd clusters inside containers](https://etcd.io/docs/v3.5/op-guide/container/)

2. Run main.go with correct etcd endpoints
   ```
   Usage: main --folder FOLDER --key KEY [--port PORT] --etcd ETCD

   Options:
   --folder FOLDER, -f FOLDER
   --key KEY, -k KEY
   --port PORT, -p PORT [default: 3000]
   --etcd ETCD
   --help, -h             display this help and exit

   go run main.go --f etcd_files -k "" --etcd <your_etcd_ip>:2379
   ```

3. Use etcd keeper to verify changes - https://github.com/evildecay/etcdkeeper