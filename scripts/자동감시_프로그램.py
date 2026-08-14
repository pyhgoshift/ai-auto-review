import time
import os
import json
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

# A2A-FAAP 폴더 설정
REQUESTS_DIR = ".a2a/requests"
AUDITS_DIR = ".a2a/audits"

# 폴더가 없으면 생성
os.makedirs(REQUESTS_DIR, exist_ok=True)
os.makedirs(AUDITS_DIR, exist_ok=True)

class A2A_Handler(FileSystemEventHandler):
    def on_created(self, event):
        if not event.is_directory and event.src_path.endswith('.md'):
            print(f"👀 [Watcher] 새로운 질문 파일 발견: {event.src_path}")
            self.process_request(event.src_path)

    def process_request(self, file_path):
        filename = os.path.basename(file_path)
        print(f"🚀 [Watcher] 다른 AI(검토자)에게 질문 전달 중... ({filename})")
        
        # 여기서 실제로는 외부 API(OpenAI/Claude) 또는 Antigravity CLI를 호출합니다.
        # 예시: subprocess.run(["agy", "run", "이 파일의 코드를 검토해줘", file_path])
        time.sleep(2) # AI가 생각하는 시간 시뮬레이션
        
        # 결과 파일 생성 (임시 목업)
        result_filename = filename.replace("질문", "답변").replace("request", "result")
        result_path = os.path.join(AUDITS_DIR, result_filename)
        
        with open(result_path, 'w', encoding='utf-8') as f:
            f.write(f"# 코드 검토 결과\n\n요청하신 `{filename}` 파일의 코드 검토를 완료했습니다.\n- 보안 문제 없음\n- 아키텍처 매우 훌륭함\n\n자동화 프로그램(Watcher)을 통해 자동 응답되었습니다.")
            
        print(f"✅ [Watcher] 답변 파일이 생성되었습니다: {result_path}")
        print(f"🔔 [Watcher] 원래 AI(작업자)에게 톡톡! (Wakeup 이벤트 발송)")

if __name__ == "__main__":
    print(f"🤖 A2A-FAAP 자동화 감시 프로그램 시작됨...")
    print(f"감시 중인 폴더: {REQUESTS_DIR}")
    
    event_handler = A2A_Handler()
    observer = Observer()
    observer.schedule(event_handler, REQUESTS_DIR, recursive=False)
    observer.start()
    
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        observer.stop()
    observer.join()
