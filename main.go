package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	dialTimeout    = 5 * time.Second
	requestTimeout = 10 * time.Second
)

var (
	etcdClient    *clientv3.Client
	fileChangeMap map[string]time.Time
)

// HTTP POST Model - /putFile
type FileModel struct {
	ETCDKey  string `json:"etcdKey"`
	FilePath string `json:"filePath"`
}

// CMD ARGS
var CMDArgs struct {
	ConfigFolder  string   `arg:"-f,--folder,required"`
	ConfigKey     string   `arg:"-k,--key,required"`
	ServerPort    int      `arg:"-p,--port" default:"3000"`
	ETCDEndpoints []string `arg:"--etcd,required"`
}

func main() {
	// Preparing ARGS
	arg.MustParse(&CMDArgs)

	// Init map
	fileChangeMap = make(map[string]time.Time)

	// ETCD Connection
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   CMDArgs.ETCDEndpoints,
		DialTimeout: dialTimeout,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"err": err,
		}).Error("error connecting to ETCD")

	}
	etcdClient = cli
	defer cli.Close()

	// ETCD Testing
	readKeyAndSaveToFolder(CMDArgs.ConfigKey, CMDArgs.ConfigFolder)
	go watchKeyAndSaveToFile(CMDArgs.ConfigKey, CMDArgs.ConfigFolder)

	// Periodic folder check
	go func() {
		for range time.Tick(15 * time.Second) {
			fileToUpload, err := walkConfigFolder(CMDArgs.ConfigFolder)
			if err != nil {
				log.WithFields(log.Fields{
					"err": err,
				}).Error("config folder walker failed")
			}
			for _, filePath := range fileToUpload {
				etcdKey, err := filepath.Rel(CMDArgs.ConfigFolder, filePath)
				if err != nil {
					log.WithFields(log.Fields{
						"filePath": filePath,
						"err":      err,
					}).Error("cannot extract etcdkey from filepath")
				}
				putFileToETCD(etcdKey, filePath)
			}
		}
	}()

	// HTTP server
	r := gin.Default()
	// Manual update file
	r.POST("/putFile", func(c *gin.Context) {
		var json FileModel
		if err := c.ShouldBindJSON(&json); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		if err := putFileToETCD(json.ETCDKey, json.FilePath); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// Manual download files
	r.POST("/downloadFile", func(c *gin.Context) {
		var json FileModel
		if err := c.ShouldBindJSON(&json); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		if err := readKeyAndSaveToFolder(json.ETCDKey, json.FilePath); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.Run(fmt.Sprintf(":%d", CMDArgs.ServerPort)) // listen and serve on 0.0.0.0:3000
}

// putFileToETCD will read filePath into string and write into ETCD using etcdKey
func putFileToETCD(etcdKey, filePath string) (err error) {
	// Reading file
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		log.WithFields(log.Fields{
			"filePath": filePath,
			"err":      err,
		}).Error("error loading file")
		return err
	}

	// Write to ETCD
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	_, err = etcdClient.Put(ctx, etcdKey, string(fileContent))
	cancel()
	if err != nil {
		log.WithFields(log.Fields{
			"etcdKey":     etcdKey,
			"err":         err,
			"fileContent": string(fileContent),
		}).Error("error putting data to ETCD")
		return err
	}
	return nil
}

// watchKeyAndSaveToFile will keep watching keys in ETCD and save relative file to fileFolder
func watchKeyAndSaveToFile(etcdKey, fileFolder string) (err error) {
	rch := etcdClient.Watch(context.Background(), etcdKey, clientv3.WithPrefix())
	for wresp := range rch {
		for _, ev := range wresp.Events {
			log.WithFields(log.Fields{
				"eventType": ev.Type,
				"etcdKey":   string(ev.Kv.Key),
			}).Info("ETCD file changed")
			filePath := filepath.Join(fileFolder, string(ev.Kv.Key))
			switch ev.Type {
			case clientv3.EventTypeDelete:
				if err := os.Remove(filePath); err != nil {
					log.WithFields(log.Fields{
						"filePath": filePath,
						"err":      err,
					}).Error("cannot delete file")
					return err
				}
			case clientv3.EventTypePut:
				fileInfo, err := saveToFolder(filePath, ev.Kv.Value)
				if err != nil {
					log.WithFields(log.Fields{
						"fileName": fileInfo.Name(),
						"err":      err,
					}).Error("cannot get file info")
				}
				fileChangeMap[filePath] = fileInfo.ModTime()
			}
		}
	}
	return nil
}

// readKeyAndSaveToFolder will read file from ETCD and save into fileFolder
func readKeyAndSaveToFolder(etcdKey, fileFolder string) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	resp, err := etcdClient.Get(ctx, etcdKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		log.WithFields(log.Fields{
			"etceKey":    etcdKey,
			"fileFolder": fileFolder,
			"err":        err,
		}).Error("cannot read key ans save to folder")
		return err
	}
	for _, ev := range resp.Kvs {
		log.WithFields(log.Fields{
			"etcdKey": string(ev.Key),
		}).Info("read key")
		filePath := filepath.Join(fileFolder, string(ev.Key))
		fileInfo, err := saveToFolder(filePath, ev.Value)
		if err != nil {
			log.WithFields(log.Fields{
				"fileName": fileInfo.Name(),
				"err":      err,
			}).Error("cannot get file info")
		}
		fileChangeMap[filePath] = fileInfo.ModTime()
	}
	return nil
}

// saveToFolder will save fileContent to filePath, if file path contain /, it will treat it as folder and
// create, ex: test/config.json will create folder test and write file into config.json
func saveToFolder(filePath string, fileContent []byte) (fileInfo os.FileInfo, err error) {
	if err := ensureDir(filepath.Dir(filePath)); err != nil {
		log.WithFields(log.Fields{
			"filePath": filePath,
			"err":      err,
		}).Error("cannot create folder")
		return nil, err
	}
	if err := os.WriteFile(filePath, fileContent, 0644); err != nil {
		log.WithFields(log.Fields{
			"filePath": filePath,
			"err":      err,
		}).Error("cannot write file")
		return nil, err
	}
	fileInfo, err = os.Stat(filePath)
	if err != nil {
		log.WithFields(log.Fields{
			"filePath": filePath,
			"err":      err,
		}).Error("cannot get file info")
		return nil, err
	}
	return fileInfo, nil
}

// ensureDir will create folder if not exist
func ensureDir(dirName string) error {
	err := os.MkdirAll(dirName, os.ModePerm)
	if err == nil {
		return nil
	}
	if os.IsExist(err) {
		// check that the existing path is a directory
		info, err := os.Stat(dirName)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("path exists but is not a directory")
		}
		return nil
	}
	return err
}

// File change monitoring

// walkConfigFolder will walk through configFolder and record last time changed to fileChangeMap
// and also return filePath string list which current modified time > last modified time recorded in fileChangeMap
func walkConfigFolder(configFolder string) (fileToUpload []string, err error) {
	err = filepath.Walk(configFolder,
		func(filePath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				if val, ok := fileChangeMap[filePath]; ok {
					if info.ModTime().After(val) {
						log.WithFields(log.Fields{
							"filePath":   filePath,
							"lastMod":    val.Local(),
							"currentMod": info.ModTime().Local(),
						}).Info("find modified local file")
						fileToUpload = append(fileToUpload, filePath)
					}
				}
				fileChangeMap[filePath] = info.ModTime()
			}
			return nil
		})
	if err != nil {
		log.WithFields(log.Fields{
			"configFolder": configFolder,
			"err":          err,
		}).Error("config walker error")
		return nil, err
	}
	return fileToUpload, nil
}
