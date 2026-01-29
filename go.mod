module github.com/calobozan/jb-serve

go 1.24.0

require (
	github.com/richinsley/jumpboot v1.0.1
	github.com/spf13/cobra v1.8.0
	github.com/spf13/pflag v1.0.5
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/richinsley/jumpboot => github.com/calobozan/jumpboot v1.0.2-fix1

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
)
