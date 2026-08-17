package rocacron

import (
	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
)

const BundledSource = "bundled:roca"

//go:embed semantic.yaml
var semantic []byte

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, BundleSpec())
}

func BundleSpec() bundledplugin.Spec {
	return bundledplugin.Spec{
		Name: Name, DatabaseFilename: DatabaseFilename, Source: BundledSource,
		Semantic: semantic, ApplySchema: ApplySchema,
	}
}

func ApplySchema(path string) error {
	service, err := Open(Options{Database: path})
	if err != nil {
		return err
	}
	return service.Close()
}
