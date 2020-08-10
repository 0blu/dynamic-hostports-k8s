# Dynamic hostports for Kubernetes

This tool will let you deploy pods with a dynamic hostport.
Sortof a polyfill for https://github.com/kubernetes/kubernetes/issues/49792

## How it works

If a new pod is being detected this tool will automatically create a nodeport service and an endpoint to this pod/port.  
The service will be created within the namespace of the pod and is also limited to the external ip of the node.

# Install

Cluster wide
``` bash
kubectl apply -f https://raw.githubusercontent.com/0blu/dynamic-hostports-k8s/master/deploy.yaml
```

If you want, you can also modify this file and use the `KUBERNETES_NAMESPACE` environment variable to limit the access.


You can also build it yourself:

``` bash
docker build -t 0blu/dynamic-hostport-manager:latest .
```

Hosted on DockerHub: https://hub.docker.com/r/0blu/dynamic-hostport-manager

# Example

This example will create 5 pods; each having 2 public servers with different outgoing (host)ports.

``` yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dynamic-hostport-example
spec:
  replicas: 5
  selector:
    matchLabels:
      app: dynamic-hostport-example-deployment
  template:
    metadata:
      annotations:
        #dynamic-hostports.k8s/8080: 'DO NOT SET' # This value will be automatically set by the tool
        #dynamic-hostports.k8s/8082: 'DO NOT SET' # you can query it to determine the outgoing hostport
      labels:
        app: dynamic-hostport-example-deployment
        # This is where the magic happens
        dynamic-hostports: '8080.8082' # Must be a string. Split multiple ports with '.'
    spec:
      containers:
      - name: dynamic-hostport-example1-container
        image: paulbouwer/hello-kubernetes:1.8
        env:
          - name: MESSAGE
            value: Hello from port 8080
          #- name: PORT
          #  value: '8080' # 8080 Is standard port of paulbouwer/hello-kubernete
        ports:
        - containerPort: 8080
          #hostPort: DO NOT SET THIS HERE
      - name: dynamic-hostport-example2-container
        image: paulbouwer/hello-kubernetes:1.8
        env:
          - name: MESSAGE
            value: Hello from port 8082
          - name: PORT
            value: '8082'
        ports:
        - containerPort: 8082
          #hostPort: DO NOT SET THIS HERE
```

## Get the port and ip

You can get the dynamically assigned hostport by querying for 'dynamic-hostports.k8s/YOURPORT' annotation

``` bash
$ kubectl get pods -l dynamic-hostports --template '{{range .items}}{{.metadata.name}}  PortA: {{index .metadata.annotations "dynamic-hostports.k8s/8080"}}  PortB: {{index .metadata.annotations "dynamic-hostports.k8s/8082"}}  Node: {{.spec.nodeName}}{{"\n"}}{{end}}'
dynamic-hostport-example-f9bf6855c-78gzd  PortA: 30535  PortB: 31011  Node: my-node-1
dynamic-hostport-example-f9bf6855c-89zxj  PortA: 32373  PortB: 30857  Node: my-node-2
dynamic-hostport-example-f9bf6855c-8qtfd  PortA: 31755  PortB: 31584  Node: my-node-1
dynamic-hostport-example-f9bf6855c-gwc9s  PortA: 30378  PortB: 31472  Node: my-node-1
dynamic-hostport-example-f9bf6855c-st7ck  PortA: 31341  PortB: 30239  Node: my-node-2
```

``` bash
$ kubectl get nodes  --template '{{range .items}}{{.metadata.name}} {{range .status.addresses}}{{.type}}: {{.address}} {{end}}{{"\n"}}{{end}}'
my-node-1 ExternalIP: xxx.xxx.xxx.xxx
my-node-2 ExternalIP: yyy.yyy.yyy.yyy

# On minikube you want to use
$ minikube ip
xxx.xxx.xxx.xxx
```

## Test it

This examples shows how to use this for _PortA_ on the _first_ pod

``` bash
$ curl http://xxx.xxx.xxx.xxx:30535
```

This examples shows how to use this for _PortB_ on the _second_ pod

``` bash
$ curl http://yyy.yyy.yyy.yyy:30857
```
