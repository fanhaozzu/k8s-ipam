
build: k8s
	GOOS=linux go build -o ./bin/k8s-ipam ./cmd

k8s: vendor/k8s.io/code-generator
	vendor/k8s.io/code-generator/generate-groups.sh all github.com/domeos/k8s-ipam/pkg/client github.com/domeos/k8s-ipam/pkg/api "ipam.k8s.io:v1alpha1"
	
vendor/k8s.io/code-generator:
	git clone -b release-1.13 https://github.com/kubernetes/code-generator vendor/k8s.io/code-generator
