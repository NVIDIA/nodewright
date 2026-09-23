/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/go-logr/logr"
	"github.com/sethvargo/go-envconfig"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	kzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/controller"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	//+kubebuilder:scaffold:imports
)

const (
	// reconcileLeaseID gates Skyhook reconciliation. Historical kubebuilder-generated
	// name; do not rename without an explicit upgrade plan.
	reconcileLeaseID = "3c22c1ae.nvidia.com"

	// webhookBootstrapLeaseID gates webhook serving-cert creation and caBundle patching.
	// Held by a SEPARATE manager from reconcileLeaseID so a stuck old leader cannot
	// deadlock the webhook cert bootstrap during a major upgrade. See
	// docs/designs/webhook-bootstrap-lease.md.
	webhookBootstrapLeaseID = "nodewright-webhook-bootstrap.nvidia.com"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(nwv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// The metrics endpoint is served by controller-runtime behind
// filters.WithAuthenticationAndAuthorization, which delegates authn/authz to the
// apiserver. These are the permissions that filter needs, not any controller's.
//+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
//+kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

type options struct {
	// SkyhookOperatorOptions are options for the operator operation, and not controller runtime.
	controller.SkyhookOperatorOptions
	controller.WebhookControllerOptions
	// MetricsPort The address the metric endpoint binds to.
	MetricsPort string `env:"METRICS_PORT, default=:8443"`
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

	// Before the manager starts, so nothing can write a legacy series first.
	if !options.PublishLegacyMetrics {
		controller.DisableLegacyMetrics()
		setupLog.Info("deprecated skyhook_* metrics disabled; only nodewright_* series are exported")
	}

	certDir := filepath.Join(os.TempDir(), "k8s-webhook-server", "serving-certs")
	restConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme,
		// No CertDir: with none on disk, controller-runtime generates an in-memory
		// self-signed cert per pod. Scrapers cannot verify it either way (Prometheus
		// targets pod IPs), so we do not manage one ourselves.
		Metrics: metricsserver.Options{
			BindAddress:    options.MetricsPort,
			SecureServing:  true,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
		},
		HealthProbeBindAddress: options.ProbePort,
		LeaderElection:         options.LeaderElection,
		LeaderElectionID:       reconcileLeaseID,
		// Scoping an informer to the operator namespace is only safe for kinds the operator
		// never reads outside it. A scoped cache does not fail closed in a useful way: a read
		// for an out-of-scope object errors at runtime, and a scoped List silently returns a
		// short answer, so widening a scope is cheap but narrowing one wrongly is a live bug.
		//
		//   Jobs        scoped. Package-stage Jobs are created in options.Namespace and every
		//               list passes client.InNamespace, so caching a cluster's CronJobs and
		//               user Jobs buys nothing.
		//   Secrets     scoped, and load-bearing: SecretCertWatcher runs on THIS manager and
		//               reads the serving-cert Secret through this cache to sync it to disk, so
		//               removing this entry breaks TLS serving on every pod. (WebhookController
		//               reads the same Secret, but on the bootstrap manager below, which scopes
		//               its own cache.) The Secret RBAC is namespaced, and informers start
		//               lazily, so an unscoped entry here opens a cluster-wide watch and gets a
		//               403 at runtime rather than a compile error.
		//
		// Deliberately NOT scoped:
		//
		//   Pods        drain must see every workload pod on a node, in any namespace.
		//               IsDrained and the drain executor list purely by the nodeName field
		//               index. Scope this and drain sees only package pods, reports the node
		//               drained while user workloads are still running, and the interrupt
		//               reboots the node under them.
		//   Nodes       cluster-scoped kind; namespaces do not apply.
		//   ConfigMaps  every access site passes client.InNamespace and a package's
		//               spec.configMap is a kubelet-resolved mount, so this looks scopable and
		//               would be the biggest memory win (ConfigMaps are numerous and up to 1MiB).
		//               Left cluster-wide for now: scoping it correlated with intermittent
		//               apply-to-config stalls in e2e/core that are not yet explained. Do not
		//               re-scope without reproducing that first.
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&batchv1.Job{}: {
					Namespaces: map[string]cache.Config{
						options.Namespace: {},
					},
				},
				&corev1.Secret{}: {
					Namespaces: map[string]cache.Config{
						options.Namespace: {},
					},
				},
			},
		},
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

	// A client-go clientset is needed only for reading pod-log streams (a subresource
	// the manager's controller-runtime client cannot serve); it shares the manager's config.
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to create clientset")
		os.Exit(1)
	}

	cont, err := controller.NewSkyhookReconciler(
		mgr.GetScheme(),
		mgr.GetClient(),
		mgr.GetAPIReader(),
		clientset,
		mgr.GetEventRecorder("nodewright-controller"),
		options.SkyhookOperatorOptions)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeWright")
		os.Exit(1)
	}
	if err = cont.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeWright")
		os.Exit(1)
	}

	// Package-stage Jobs and their pods get their own controllers rather than prefixed
	// requests on the shared reconcile queue: both reconcile per-object, so a real watch
	// gives each a real requeue and its own backoff.
	if err = controller.NewJobReconciler(mgr.GetClient(), mgr.GetAPIReader(), clientset, mgr.GetEventRecorder("job-controller"), options.JobOperatorOptions).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Job")
		os.Exit(1)
	}
	if err = controller.NewPodReconciler(mgr.GetClient(), mgr.GetAPIReader(), clientset, mgr.GetEventRecorder("pod-controller"), options.Namespace).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Pod")
		os.Exit(1)
	}

	// Mirror controllers import legacy skyhook.nvidia.com objects into the new
	// nodewright.nvidia.com group during the migration bridge (one-way, level-triggered).
	if err = (&controller.SkyhookMirrorReconciler{}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SkyhookMirror")
		os.Exit(1)
	}
	if err = (&controller.DeploymentPolicyMirrorReconciler{}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DeploymentPolicyMirror")
		os.Exit(1)
	}

	// The webhook cert/caBundle bootstrap runs on its own lease so a stuck old-version
	// leader on reconcileLeaseID cannot block a new pod from issuing the serving cert and
	// patching the webhook configurations. Its manager is built per attempt rather than
	// once, because relinquishing that lease means stopping the manager that holds it; see
	// runWebhookBootstrap below and docs/designs/webhook-bootstrap-lease.md.
	var webhookController *controller.WebhookController
	newWebhookBootstrapMgr := func() (ctrl.Manager, error) {
		bootstrapMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
			Scheme: scheme,
			// Disable metrics + health probes on the bootstrap manager; the main manager
			// already serves both, and binding the same ports twice would fail.
			Metrics:          metricsserver.Options{BindAddress: "0"},
			LeaderElection:   options.LeaderElection,
			LeaderElectionID: webhookBootstrapLeaseID,
			// This manager owns the only Secret watch, and its Secret RBAC is namespaced, so the
			// informer must be too: a cluster-wide LIST/WATCH would be rejected and the webhook
			// would never bootstrap. Safe because every read is the operator's own serving-cert
			// Secret — the For predicate matches on namespace and name, and webhookConfigToSecret
			// enqueues that same key. The webhook configurations watched below are cluster-scoped
			// kinds, so this does not touch them.
			Cache: cache.Options{
				ByObject: map[client.Object]cache.ByObject{
					&corev1.Secret{}: {
						Namespaces: map[string]cache.Config{
							options.Namespace: {},
						},
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("building webhook bootstrap manager: %w", err)
		}
		// WebhookController runs only on the holder of webhookBootstrapLeaseID.
		if err := bootstrapMgr.Add(webhookController); err != nil {
			return nil, fmt.Errorf("adding webhook controller as runnable: %w", err)
		}
		if err := webhookController.SetupWithManager(bootstrapMgr); err != nil {
			return nil, fmt.Errorf("setting up webhook controller: %w", err)
		}
		return bootstrapMgr, nil
	}

	if options.EnableWebhooks {
		// The bootstrap controller reads through an UNCACHED client, unlike every other
		// controller here. Two reasons, both from it outliving any one manager:
		//
		//   - It is driven by the bootstrap manager's watches but shares its instance with
		//     the readyz check on the main manager, and it has to keep answering across a
		//     bootstrap manager restart, which takes that manager's cache down with it.
		//     A cached client from either manager makes the reader's lifetime the wrong
		//     shape for one of the two callers.
		//   - Reading from a cache that is not the one that delivered the event means the
		//     read can lag the event. This controller creates Secret/webhook-cert and is
		//     then woken by its own create; a stale cache answers NotFound, the reconcile
		//     returns early, and nothing wakes it again because the object never changes
		//     a second time. Bootstrap has to read its own writes.
		//
		// The cost is a handful of direct reads per reconcile and per readiness probe,
		// against small cluster-scoped collections, on a controller that otherwise sits
		// idle behind a 24h requeue.
		apiClient, err := client.New(restConfig, client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "unable to build uncached client for the webhook controller")
			os.Exit(1)
		}
		webhookController, err = controller.NewWebhookController(
			apiClient,
			mgr.GetCache(),
			options.Namespace,
			certDir,
			options.WebhookControllerOptions,
		)
		if err != nil {
			setupLog.Error(err, "unable to create webhook controller", "controller", "Webhook")
			os.Exit(1)
		}

		// SecretCertWatcher runs on every pod (NeedLeaderElection=false) and syncs the
		// bootstrapped Secret to disk so the webhook server on this pod can serve TLS.
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

		// Admission webhook handlers serve from the main manager's webhook server.
		if err = (&v1alpha1.Skyhook{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Skyhook")
			os.Exit(1)
		}
		if err = (&v1alpha1.DeploymentPolicy{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "DeploymentPolicy")
			os.Exit(1)
		}

		// The nodewright.nvidia.com webhook configs are fail-closed, so their handlers
		// must be registered too or every NodeWright/DeploymentPolicy write is rejected.
		if err = (&nwv1.NodeWright{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "NodeWright")
			os.Exit(1)
		}
		if err = (&nwv1.DeploymentPolicy{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "DeploymentPolicy (nodewright)")
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
	ctx := ctrl.SetupSignalHandler()
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := mgr.Start(gctx); err != nil {
			return fmt.Errorf("reconcile manager: %w", err)
		}
		return nil
	})
	if webhookController != nil {
		g.Go(func() error {
			return runWebhookBootstrap(gctx, webhookController, newWebhookBootstrapMgr)
		})
	}
	if err := g.Wait(); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// webhookBootstrapRecontendDelay is how long to wait after relinquishing the webhook
// bootstrap lease before contending for it again. Long enough that the pod this operator
// stood aside for wins the election and gets its work done, short enough that a pod which
// stood aside for nobody is not idle for long.
const webhookBootstrapRecontendDelay = 30 * time.Second

// runWebhookBootstrap keeps a webhook bootstrap manager running for as long as this
// operator can usefully hold the bootstrap lease.
//
// The manager is the thing that holds the lease, so relinquishing it means stopping the
// manager and building a fresh one to contend again. The controller asks for that when it
// finds that every webhook configuration in its namespace now dials some other Service:
// the release has moved on without this pod, and a pod that can reach those configurations
// is blocked behind this lease (#469). Standing aside is safe in every direction. If no
// other pod takes the lease, this one takes it back a moment later and carries on; the
// main manager, and therefore admission serving and readiness, is untouched either way.
func runWebhookBootstrap(ctx context.Context, webhookController *controller.WebhookController, newMgr func() (ctrl.Manager, error)) error {
	for {
		bootstrapMgr, err := newMgr()
		if err != nil {
			return fmt.Errorf("webhook bootstrap manager: %w", err)
		}

		bootstrapCtx, cancel := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- bootstrapMgr.Start(bootstrapCtx) }()

		select {
		case err := <-stopped:
			cancel()
			if ctx.Err() != nil {
				return nil // shutting down
			}
			if err != nil {
				return fmt.Errorf("webhook bootstrap manager: %w", err)
			}
			return nil
		case <-webhookController.Relinquished():
			cancel()
			// Wait for the manager to finish stopping before contending again, so two
			// elections for the same lease are never in flight from this process. A
			// cancelled manager returns nil, so anything else here means it stopped for a
			// reason of its own and this loop is about to paper over it by recontending.
			if err := <-stopped; err != nil {
				setupLog.Error(err, "webhook bootstrap manager stopped with an error while relinquishing the lease")
			}
			// A reconcile that was already in flight during that shutdown can signal
			// again, and the signal is buffered. Left there, the next manager would be
			// cancelled the instant it started, before running a single reconcile, and
			// this loop would never hold the lease long enough to do anything even after
			// the condition cleared. Discard anything stale: the next manager decides for
			// itself, from its own reconcile.
			webhookController.DrainRelinquished()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(webhookBootstrapRecontendDelay):
		}
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
