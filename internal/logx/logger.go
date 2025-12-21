package logx

import (
	"log"
	"os"
	"sync"
)

var (
	logger *log.Logger
	mu     sync.Mutex
)

// Init
// 初始化日志输出到指定文件
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	logger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
}

// Info 普通日志
func Info(msg string) {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		logger.Println(msg)
	}
}

// Error 错误日志
func Error(tag string, err error) {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		logger.Println("ERROR", tag, err)
	}
}

// Major 重大事故日志
func Major(tag string, v any) {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		logger.Println("MAJOR", tag, v)
	}
}
