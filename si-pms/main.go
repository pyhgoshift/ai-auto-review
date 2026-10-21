// si-pms: SI 프로젝트 투입 인력·PC·보안 통합 관리
//
// 실행파일 하나와 데이터 폴더 하나로 동작한다(폐쇄망 반입용).
//
//	si-pms -addr :8080 -data ./data
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "수신 주소")
	dataDir := flag.String("data", "data", "데이터 폴더(DB, 업로드 양식, 백업)")
	cert := flag.String("cert", "", "TLS 인증서 파일(선택)")
	key := flag.String("key", "", "TLS 개인키 파일(선택)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	db, err := openDB(filepath.Join(*dataDir, "pms.db"))
	if err != nil {
		log.Fatal(err)
	}
	app := &App{db: db, dataDir: *dataDir, secureCookie: *cert != ""}
	app.loadTemplates()

	pw, err := app.ensureAdmin()
	if err != nil {
		log.Fatal(err)
	}
	if pw != "" {
		fmt.Println("==============================================")
		fmt.Println(" 초기 관리자 계정이 생성되었습니다.")
		fmt.Println("   아이디: admin")
		fmt.Println("   임시 비밀번호:", pw)
		fmt.Println(" 첫 로그인 시 비밀번호를 변경해야 합니다.")
		fmt.Println("==============================================")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
	}
	log.Printf("si-pms 시작: %s (데이터: %s)", *addr, *dataDir)
	if *cert != "" {
		log.Fatal(srv.ListenAndServeTLS(*cert, *key))
	}
	log.Fatal(srv.ListenAndServe())
}
