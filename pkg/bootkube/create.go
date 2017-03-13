package bootkube

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	apierrors "k8s.io/kubernetes/pkg/api/errors"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_5"
	"k8s.io/kubernetes/pkg/client/restclient"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	clientcmdapi "k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/resource"
	"k8s.io/kubernetes/pkg/util/wait"

	"github.com/kubernetes-incubator/bootkube/pkg/util"
)

func CreateAssets(manifestDir string, timeout time.Duration) error {

	upFn := func() (bool, error) {
		if err := apiTest(); err != nil {
			glog.Warningf("Unable to determine api-server readiness: %v", err)
			return false, nil
		}
		return true, nil
	}

	createFn := func() (bool, error) {
		err := createAssets(manifestDir)
		if err != nil {
			err = fmt.Errorf("Error creating assets: %v", err)
			glog.Error(err)
			UserOutput("%v\n", err)
			UserOutput("\nNOTE: Bootkube failed to create some cluster assets. It is important that manifest errors are resolved and resubmitted to the apiserver.\n")
			UserOutput("For example, after resolving issues: kubectl create -f <failed-manifest>\n\n")
		}

		// Do not fail cluster creation due to missing assets as it is a recoverable situation
		// See https://github.com/kubernetes-incubator/bootkube/pull/368/files#r105509074
		return true, nil
	}

	UserOutput("Waiting for api-server...\n")
	start := time.Now()
	if err := wait.Poll(5*time.Second, timeout, upFn); err != nil {
		err = fmt.Errorf("API Server is not ready: %v", err)
		glog.Error(err)
		return err
	}

	UserOutput("Creating self-hosted assets...\n")
	timeout = timeout - time.Since(start)
	if err := wait.PollImmediate(5*time.Second, timeout, createFn); err != nil {
		err = fmt.Errorf("Failed to create assets: %v", err)
		glog.Error(err)
		return err
	}

	return nil
}

func createAssets(manifestDir string) error {
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{},
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: insecureAPIAddr}},
	)
	f := cmdutil.NewFactory(config)

	shouldValidate := true
	schema, err := f.Validator(shouldValidate, fmt.Sprintf("~/%s/%s", clientcmd.RecommendedHomeDir, clientcmd.RecommendedSchemaName))
	if err != nil {
		return err
	}

	cmdNamespace, enforceNamespace, err := f.DefaultNamespace()
	if err != nil {
		return err
	}

	mapper, typer := f.Object()

	filenameOpts := &resource.FilenameOptions{
		Filenames: []string{manifestDir},
		Recursive: false,
	}

	r := resource.NewBuilder(mapper, typer, resource.ClientMapperFunc(f.ClientForMapping), f.Decoder(true)).
		Schema(schema).
		ContinueOnError().
		NamespaceParam(cmdNamespace).DefaultNamespace().
		FilenameParam(enforceNamespace, filenameOpts).
		Flatten().
		Do()
	err = r.Err()
	if err != nil {
		return err
	}

	count := 0
	err = r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}

		obj, err := resource.NewHelper(info.Client, info.Mapping).Create(info.Namespace, true, info.Object)
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return cmdutil.AddSourceToErr("creating", info.Source, err)
		}
		info.Refresh(obj, true)

		count++
		shortOutput := false
		if !shortOutput {
			f.PrintObjectSpecificMessage(info.Object, util.GlogWriter{})
		}
		cmdutil.PrintSuccess(mapper, shortOutput, util.GlogWriter{}, info.Mapping.Resource, info.Name, false, "created")
		UserOutput("\tcreated %23s %s\n", info.Name, strings.TrimSuffix(info.Mapping.Resource, "s"))
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no objects passed to create")
	}
	return nil
}

func apiTest() error {
	client, err := clientset.NewForConfig(&restclient.Config{Host: insecureAPIAddr})
	if err != nil {
		return err
	}

	// API Server is responding
	_, err = client.Discovery().ServerVersion()
	if err != nil {
		return err
	}

	// System namespace has been created
	_, err = client.Namespaces().Get(api.NamespaceSystem)
	return err
}
