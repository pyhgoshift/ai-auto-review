# 설치 출처

- 원본: https://github.com/mvanhorn/last30days-skill (MIT License, `LICENSE` 참고)
- 버전: v4.6.0 / commit `331025e662756ca51e86dfddcf59969f6dba25a0`
- 원본 `skills/last30days/`에서 런타임 파일만 복사 (데모 이미지·mp3 `assets/`, 개발용 테스트 스크립트 제외)

## 업데이트 방법

```bash
git clone --depth 1 https://github.com/mvanhorn/last30days-skill /tmp/l30
rsync -a --delete --exclude assets --exclude INSTALLED_FROM.md --exclude LICENSE \
  /tmp/l30/skills/last30days/ .claude/skills/last30days/
```
