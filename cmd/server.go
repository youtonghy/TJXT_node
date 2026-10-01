package cmd

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
	"github.com/youtonghy/TJXT_node/node"
)

var (
	config string
	watch  bool
)

// Backoff between attempts to start again after a reload tore the old
// instance down and the new one failed to start.
const (
	restartBackoffMin = time.Second
	restartBackoffMax = time.Minute
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run the tjxt-node server",
	Run:   serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/tjxt-node/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

// instance is one running generation of nodes and their Xray core.
type instance struct {
	nodes *node.Node
	core  *core.V2Core
}

// prepared is a loaded and validated configuration whose node info has been
// fetched and whose Xray config builds, ready to replace the running instance.
type prepared struct {
	conf  *conf.Conf
	nodes *node.Node
}

func serverHandle(_ *cobra.Command, _ []string) {
	showVersion()
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: true,
		DisableQuote:     true,
		PadLevelText:     false,
	})
	//init limiter
	limiter.Init()
	next, err := prepare(config)
	if err != nil {
		log.WithField("err", err).Error("Load config or node info failed")
		return
	}
	applyLogConfig(next.conf)
	// Enable pprof if configured
	if next.conf.PprofPort != 0 {
		go func() {
			log.Infof("Starting pprof server on :%d", next.conf.PprofPort)
			if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", next.conf.PprofPort), nil); err != nil {
				log.WithField("err", err).Error("pprof server failed")
			}
		}()
	}
	log.Info("Got nodes info from server")
	var reloadCh = make(chan struct{}, 1)
	current, err := next.start(reloadCh)
	if err != nil {
		log.WithField("err", err).Error("Run nodes failed")
		return
	}
	log.Info("Nodes started")
	if watch {
		// On file change, just signal reload; do not run reload concurrently here
		w, err := conf.Watch(config, func() {
			select {
			case reloadCh <- struct{}{}:
			default: // drop if a reload is already queued
			}
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			current.close()
			return
		}
		defer func() {
			if err := w.Close(); err != nil {
				log.WithField("err", err).Error("Close config watcher failed")
			}
		}()
	}
	// clear memory
	runtime.GC()

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)

	// running is the configuration of current. retry re-attempts a reload that
	// failed: while the old (or rolled-back) instance keeps serving, or while
	// nothing runs after a rollback also failed.
	running := next
	var retry <-chan time.Time
	backoff := restartBackoffMin
	scheduleRetry := func(err error) {
		log.WithField("err", err).Errorf("重启失败, retrying in %s", backoff)
		retry = time.After(backoff)
		backoff = min(backoff*2, restartBackoffMax)
	}
	attemptReload := func() {
		retry = nil
		next, err := prepare(config)
		if err != nil {
			if current != nil {
				log.Warn("new config rejected, keeping the running one")
			}
			scheduleRetry(err)
			return
		}
		applyLogConfig(next.conf)
		current.close()
		current = nil
		if current, err = next.start(reloadCh); err == nil {
			log.Info("重启成功")
			running = next
			backoff = restartBackoffMin
			runtime.GC()
			return
		}
		startErr := err
		if running != nil {
			// The old instance is gone already; bring the last working
			// configuration back from its cached node info.
			if inst, again, rerr := running.restart(reloadCh); rerr == nil {
				current, running = inst, again
				log.WithField("err", startErr).Error("new config failed to start, rolled back to the previous one")
			} else {
				log.WithField("err", rerr).Error("rollback to the previous config failed")
			}
		}
		scheduleRetry(startErr)
	}
	for {
		select {
		case <-osSignals:
			log.Info("收到退出信号，正在关闭程序...")
			// Closing the nodes flushes unreported traffic to the panel.
			current.close()
			return
		case <-reloadCh:
			log.Info("收到重启信号，正在重新加载配置...")
			attemptReload()
		case <-retry:
			attemptReload()
		}
	}
}

// prepare loads, validates and resolves a configuration without touching the
// running instance.
func prepare(path string) (*prepared, error) {
	c := conf.New()
	if err := c.LoadFromPath(path); err != nil {
		return nil, err
	}
	nodes, err := node.New(c.NodeConfigs)
	if err != nil {
		return nil, fmt.Errorf("get node info: %w", err)
	}
	if _, err := core.BuildConfig(c, nodes.NodeInfos); err != nil {
		return nil, fmt.Errorf("build xray config: %w", err)
	}
	return &prepared{conf: c, nodes: nodes}, nil
}

// start starts the core and the nodes; on failure everything started is
// closed again.
func (p *prepared) start(reloadCh chan struct{}) (*instance, error) {
	v2core := core.New(p.conf)
	v2core.ReloadCh = reloadCh
	if err := v2core.Start(p.nodes.NodeInfos); err != nil {
		return nil, fmt.Errorf("start core: %w", err)
	}
	inst := &instance{nodes: p.nodes, core: v2core}
	if err := p.nodes.Start(p.conf.NodeConfigs, v2core); err != nil {
		inst.close()
		return nil, fmt.Errorf("start nodes: %w", err)
	}
	return inst, nil
}

// restart starts the same configuration again with fresh controllers built
// from the cached node info, without asking the panel.
func (p *prepared) restart(reloadCh chan struct{}) (*instance, *prepared, error) {
	nodes, err := node.FromInfos(p.conf.NodeConfigs, p.nodes.NodeInfos)
	if err != nil {
		return nil, nil, err
	}
	again := &prepared{conf: p.conf, nodes: nodes}
	inst, err := again.start(reloadCh)
	if err != nil {
		return nil, nil, err
	}
	return inst, again, nil
}

// close stops the nodes (flushing traffic) and then the core, logging errors.
func (i *instance) close() {
	if i == nil {
		return
	}
	if err := i.nodes.Close(); err != nil {
		log.WithField("err", err).Error("Close nodes failed")
	}
	if err := i.core.Close(); err != nil {
		log.WithField("err", err).Error("Close core failed")
	}
}

func applyLogConfig(c *conf.Conf) {
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn", "warning":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if c.LogConfig.Output == "" {
		return
	}
	f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.WithField("err", err).Error("Open log file failed, keeping the current log output")
		return
	}
	old := log.StandardLogger().Out
	log.SetOutput(f)
	// 关闭旧的日志文件（如果是文件）
	if oldFile, ok := old.(*os.File); ok && oldFile != os.Stdout && oldFile != os.Stderr {
		if err := oldFile.Close(); err != nil {
			log.WithField("err", err).Warn("Close previous log file failed")
		}
	}
}
