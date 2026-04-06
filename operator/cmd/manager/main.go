/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/go-logr/logr"
	"github.com/sethvargo/go-envconfig"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	kzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/controller"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

type options struct {
	// SkyhookOperatorOptions are options for the operator operation, and not controller runtime.
	controller.SkyhookOperatorOptions
	controller.WebhookControllerOptions
	// MetricsPort The address the metric endpoint binds to.
	MetricsPort string `env:"METRICS_PORT, default=:8080"`
	// ProbePort The address the probe endpoint binds to.
	ProbePort string `env:"PROBE_PORT, default=:8081"`
	// LeaderElection Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.
	LeaderElection bool `env:"LEADER_ELECTION, default=false"`
	// EnableWebhooks Enables running of the webhook server, useful to disable for development
	EnableWebhooks bool `env:"ENABLE_WEBHOOKS, default=true"`

	// zap logger settings, try to expose things from BindFlags into ENVs
	LogEncoder      string `env:"LOG_ENCODER, default=json"`           // 'json' or 'console'
	LogLevel        string `env:"LOG_LEVEL, default=debug"`            // 'debug', 'info', 'error' or or any integer value > 0 which corresponds to custom debug levels of increasing verbosity
	StackTraceLevel string `env:"LOG_STACK_TRACE_LEVEL, default=warn"` // Level at and above which stacktraces are captured (one of 'info', 'error', 'panic').
	TimeEncoder     string `env:"LOG_TIME_ENCODER, default=rfc3339"`   // Zap time encoding (one of 'epoch', 'millis', 'nano', 'iso8601', 'rfc3339' or 'rfc3339nano').
}

func main() {
	var options options
	if err := envconfig.Process(context.TODO(), &options); err != nil {
		log.Fatal(err)
	}

	ctrl.SetLogger(logger(options))
	setupLog.Info("env options", "options", options)

	certDir := filepath.Join(os.TempDir(), "k8s-webhook-server", "serving-certs")
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: options.MetricsPort},
		HealthProbeBindAddress: options.ProbePort,
		LeaderElection:         options.LeaderElection,
		LeaderElectionID:       "3c22c1ae.nvidia.com",
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:       9443,
			CertDir:    certDir,
			CertName:   "tls.crt",
			KeyName:    "tls.key",
			WebhookMux: http.NewServeMux(),
		}),
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cont, err := controller.NewSkyhookReconciler(
		mgr.GetScheme(),
		mgr.GetClient(),
		mgr.GetEventRecorderFor("skyhook-controller"),
		options.SkyhookOperatorOptions)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Skyhook")
		os.Exit(1)
	}
	if err = cont.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Skyhook")
		os.Exit(1)
	}

	if options.EnableWebhooks {
		webhookController, err := controller.NewWebhookController(
			mgr.GetClient(),
			mgr.GetCache(),
			options.Namespace,
			certDir,
			options.WebhookControllerOptions,
		)

		if err != nil {
			setupLog.Error(err, "unable to create webhook controller", "controller", "Webhook")
			os.Exit(1)
		}

		// Add webhook controller as a leader runnable
		if err = mgr.Add(webhookController); err != nil {
			setupLog.Error(err, "unable to add webhook controller as runnable", "controller", "Webhook")
			os.Exit(1)
		}

		// Add secret cert watcher as runnable
		if err := mgr.Add(controller.NewSecretCertWatcher(
			mgr.GetClient(),
			mgr.GetCache(),
			options.Namespace,
			options.WebhookControllerOptions.SecretName,
			certDir,
		)); err != nil {
			setupLog.Error(err, "unable to add secret cert watcher", "controller", "SecretCertWatcher")
			os.Exit(1)
		}

		// Set up the webhook controller with the manager
		if err = webhookController.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook controller", "controller", "Webhook")
			os.Exit(1)
		}

		// Set up the webhook for Skyhook
		if err = (&v1alpha1.Skyhook{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Skyhook")
			os.Exit(1)
		}

		// Set up the webhook for DeploymentPolicy
		if err = (&v1alpha1.DeploymentPolicy{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "DeploymentPolicy")
			os.Exit(1)
		}

		if err := mgr.AddReadyzCheck("readyz", webhookController.WebhookSecretReadyzCheck); err != nil {
			setupLog.Error(err, "unable to set up ready check")
			os.Exit(1)
		}
	} else {
		// cant have this and the one above, does not work
		if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up ready check")
			os.Exit(1)
		}
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// logger helper for setting up the (k)zap logger using envs instead of using flags
func logger(options options) logr.Logger {

	opts := make([]kzap.Opts, 0)

	// odd... dont like having to do this myself, but its done now
	opts = append(opts, func(o *kzap.Options) {
		switch strings.ToLower(options.LogEncoder) {
		case "console":
			o.NewEncoder = func(opts ...kzap.EncoderConfigOption) zapcore.Encoder {
				encoderConfig := zap.NewDevelopmentEncoderConfig()
				for _, opt := range opts {
					opt(&encoderConfig)
				}
				return zapcore.NewConsoleEncoder(encoderConfig)
			}
		case "json":
			fallthrough
		default:
			o.NewEncoder = func(opts ...kzap.EncoderConfigOption) zapcore.Encoder {
				encoderConfig := zap.NewProductionEncoderConfig()
				for _, opt := range opts {
					opt(&encoderConfig)
				}
				return zapcore.NewJSONEncoder(encoderConfig)
			}
		}
	})

	lvl, err := zapcore.ParseLevel(options.LogLevel)
	if err != nil {
		panic(err)
	}

	opts = append(opts, kzap.Level(zap.NewAtomicLevelAt(lvl)))
	lvl, err = zapcore.ParseLevel(options.StackTraceLevel)
	if err != nil {
		panic(err)
	}
	opts = append(opts, kzap.StacktraceLevel(zap.NewAtomicLevelAt(lvl)))

	// again pretty odd i could not find a func for this... UnmarshalText is close
	opts = append(opts, func(o *kzap.Options) {
		switch options.TimeEncoder {
		case "rfc3339nano", "RFC3339Nano":
			o.TimeEncoder = zapcore.RFC3339NanoTimeEncoder
		case "rfc3339", "RFC3339":
			o.TimeEncoder = zapcore.RFC3339TimeEncoder
		case "iso8601", "ISO8601":
			o.TimeEncoder = zapcore.ISO8601TimeEncoder
		case "millis":
			o.TimeEncoder = zapcore.EpochMillisTimeEncoder
		case "nanos":
			o.TimeEncoder = zapcore.EpochNanosTimeEncoder
		default:
			o.TimeEncoder = zapcore.EpochTimeEncoder
		}

	})

	logger := kzap.New(opts...)
	if version.GIT_SHA != "" {
		logger = logger.WithValues("git_sha", version.GIT_SHA)
	}
	if version.VERSION != "" {
		logger = logger.WithValues("version", version.VERSION)
	}

	return logger
}
