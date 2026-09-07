# bb 설치·업그레이드·제거

사용자 계정으로 체크아웃 안에서 `bash manage.sh install|upgrade|uninstall`을 실행한다.
`install`과 `upgrade`는 같은 검증된 릴리스 설치기를 사용한다. Go 빌드는 필요 없으며
비공개 릴리스는 로그인된 GitHub CLI가 필요하다. `--github-cli` 또는 `--public`으로
다운로드 방식을 명시할 수 있다.

```sh
bash manage.sh install
bb version
bash manage.sh upgrade
bb version
```

버전 출력이 설치기가 알린 버전과 일치하면 적용 완료다. 기존 바이너리는 설치기가 출력한
`.bb.backup.*`에 남는다. 다운로드·체크섬 실패는 기존 바이너리를 교체하지 않는다.
이 명령은 릴리스를 설치하며 개발 저장소의 미출시 코드를 빌드하거나 Git pull하지 않는다.

다른 버전으로 복구하려면 `bash manage.sh install --version <버전>`을 사용한다.
`<버전>`은 실제 릴리스 번호로 바꾼다. 네트워크가 끊겼다면 출력된 바이너리 백업을
설치 위치로 복사할 수 있다. 이 경우 관리 해시가 달라지므로 다음 제거 전 검증된 릴리스를
다시 설치해야 한다.

```sh
bash manage.sh uninstall
```

설치 때 기록한 해시와 실제 파일이 일치해야 제거한다. 일치하지 않거나 관리 기록이 없으면
중단하므로 해당 파일의 출처를 확인한다. 제거된 바이너리는 출력된 `.bb.uninstalled.*`에
보관한다. 설정, 암호화 비밀, 상태와 셸 설정은 삭제하지 않는다. 기존 셸 함수는 새 셸에서
사라지며, `.zshrc`의 `bb shell init` 호출은 삭제하거나 `command -v bb` 조건으로 감싼다.
제거 후 `command -v bb`에서 다른 경로가 보이면 별도로 설치된 bb가 남아 있는 것이다.

`--install-dir`로 설치했다면 모든 후속 명령에 같은 경로를 사용한다. 기본값은
`XDG_BIN_HOME`, 없으면 `~/.local/bin`이다. 이 디렉터리를 PATH에 넣어야 `bb`를 실행할 수 있다.
설정과 비밀 데이터의 완전 삭제는 이 명령에 포함하지 않는다.
