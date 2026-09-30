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
	Short: "Run v2node server",
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

	// retry fires while no instance runs, i.e. after a reload tore the old
	// instance down and starting the new one failed.
	var retry <-chan time.Time
	backoff := restartBackoffMin
	for {
		select {
		case <-osSignals:
			log.Info("收到退出信号，正在关闭程序...")
			// Closing the nodes flushes unreported traffic to the panel.
			current.close()
			return
		case <-reloadCh:
			log.Info("收到重启信号，正在重新加载配置...")
			if current != nil {
				next, err := prepare(config)
				if err != nil {
					log.WithField("err", err).Error("重启失败: new config rejected, keeping the running one")
					continue
				}
				applyLogConfig(next.conf)
				current.close()
				current, err = next.start(reloadCh)
				if err == nil {
					log.Info("重启成功")
					backoff = restartBackoffMin
					retry = nil
					runtime.GC()
					continue
				}
				log.WithField("err", err).Errorf("重启失败, retrying in %s", backoff)
				retry = time.After(backoff)
				continue
			}
			// A reload request while down retries right away.
			retry = time.After(0)
		case <-retry:
			retry = nil
			if current, err = startFresh(reloadCh); err == nil {
				log.Info("重启成功")
				backoff = restartBackoffMin
				runtime.GC()
				continue
			}
			backoff = min(backoff*2, restartBackoffMax)
			log.WithField("err", err).Errorf("重启失败, retrying in %s", backoff)
			retry = time.After(backoff)
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

func startFresh(reloadCh chan struct{}) (*instance, error) {
	next, err := prepare(config)
	if err != nil {
		return nil, err
	}
	applyLogConfig(next.conf)
	return next.start(reloadCh)
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
