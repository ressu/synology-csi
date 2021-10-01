/*
 * Copyright 2018 Ji-Young Park(jiyoung.park.dev@gmail.com)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/jparklab/synology-csi/cmd/syno-csi-plugin/options"
	"github.com/jparklab/synology-csi/pkg/driver"
)

var (
	version = "(dev)"
	commit  = "none"
	date    = "unknown"
)

// initKlog is a workaround to initialize klog while glog is still being used
// TODO: remove the below workaround with this once glog is gone
func initKlog() {
	klogFlags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(klogFlags)

	// Sync the glog and klog flags.
	flag.CommandLine.VisitAll(func(f1 *flag.Flag) {
		f2 := klogFlags.Lookup(f1.Name)
		if f2 != nil {
			value := f1.Value.String()
			f2.Value.Set(value)
		}
	})
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Synology CSI plugin version %v, Build date: %s, Commit ID: %s\n", version, date, commit)
	},
}

func main() {
	runOptions := options.NewRunOptions()

	rootCmd := &cobra.Command{
		Use:  "synology-csi-plugin",
		Long: "Synology CSI(Container Storage Interface) plugin",
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := runOptions.Endpoint
			nodeID := runOptions.NodeID

			initKlog()

			klog.V(1).Infof("Synology CSI plugin version %s starting.", version)

			synoOption, err := options.ReadConfig(runOptions.SynologyConf)
			if err != nil {
				fmt.Printf("Failed to read config: %v\n", err)
				return err
			}

			if runOptions.CheckLogin {
				_, _, err := driver.Login(synoOption)
				if err != nil {
					fmt.Printf("Failed to login: %v\n", err)
				}
				return err
			}

			drv, err := driver.NewDriver(nodeID, endpoint, version, synoOption)
			if err != nil {
				fmt.Printf("Failed to create driver: %v\n", err)
				return err
			}
			drv.Run()

			return nil
		},
		SilenceUsage: true,
	}

	rootCmd.AddCommand(versionCmd)
	runOptions.AddFlags(rootCmd, rootCmd.PersistentFlags())

	// TODO: enable this once glog has been removed and remove initKlog above
	// klog.InitFlags(rootCmd.PersistentFlags())

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}

	os.Exit(0)
}
