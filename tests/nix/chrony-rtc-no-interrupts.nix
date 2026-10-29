# pkgs/overlays/chrony.nix next to a stock chronyd. A preloaded shim stands in
# for the RTC: it accepts the update interrupt request and reads the time, but
# only interrupts once a second when FAKE_RTC_TICK is set. Without interrupts
# the patched chronyd -s sends READY after its 20 s limit and the stock one
# never does; with interrupts the patched one finishes the initial trim as
# before. When the stock one sends READY without interrupts too, the test says
# to drop the patch.
{ pkgs, stockChrony }:
let
  shim = pkgs.runCommandCC "fake-rtc-shim" { } ''
    mkdir -p $out/lib
    cat > shim.c <<'EOF'
    #define _GNU_SOURCE
    #include <dlfcn.h>
    #include <fcntl.h>
    #include <linux/rtc.h>
    #include <pthread.h>
    #include <stdarg.h>
    #include <stdlib.h>
    #include <string.h>
    #include <time.h>
    #include <unistd.h>

    /* read end handed to chronyd, write end used by the tick thread */
    static int fake[16][2];
    static int nfake;
    static volatile int uie;
    static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;

    static int find_fake(int fd)
    {
      for (int i = 0; i < nfake; i++)
        if (fake[i][0] == fd)
          return i;
      return -1;
    }

    static void *tick(void *arg)
    {
      (void)arg;
      for (;;) {
        sleep(1);
        if (!uie)
          continue;
        unsigned long v = RTC_UF | RTC_IRQF | (1 << 8);
        pthread_mutex_lock(&lock);
        for (int i = 0; i < nfake; i++)
          (void)!write(fake[i][1], &v, sizeof v);
        pthread_mutex_unlock(&lock);
      }
      return NULL;
    }

    static int open_fake(const char *path)
    {
      const char *dev = getenv("FAKE_RTC");
      int p[2];
      if (!dev || strcmp(path, dev) != 0 || nfake == 16 || pipe(p) < 0)
        return -2;
      pthread_mutex_lock(&lock);
      fake[nfake][0] = p[0];
      fake[nfake][1] = p[1];
      nfake++;
      pthread_mutex_unlock(&lock);
      return p[0];
    }

    int close(int fd)
    {
      static int (*real)(int);
      if (!real)
        real = dlsym(RTLD_NEXT, "close");
      pthread_mutex_lock(&lock);
      int i = find_fake(fd);
      if (i >= 0) {
        real(fake[i][1]);
        fake[i][0] = fake[nfake - 1][0];
        fake[i][1] = fake[nfake - 1][1];
        nfake--;
      }
      pthread_mutex_unlock(&lock);
      return real(fd);
    }

    int open(const char *path, int flags, ...)
    {
      static int (*real)(const char *, int, ...);
      int fd = open_fake(path);
      if (fd != -2)
        return fd;
      if (!real)
        real = dlsym(RTLD_NEXT, "open");
      va_list ap;
      va_start(ap, flags);
      mode_t mode = va_arg(ap, mode_t);
      va_end(ap);
      return real(path, flags, mode);
    }

    int open64(const char *path, int flags, ...)
    {
      va_list ap;
      va_start(ap, flags);
      mode_t mode = va_arg(ap, mode_t);
      va_end(ap);
      return open(path, flags, mode);
    }

    int ioctl(int fd, unsigned long req, ...)
    {
      static int (*real)(int, unsigned long, ...);
      va_list ap;
      va_start(ap, req);
      void *arg = va_arg(ap, void *);
      va_end(ap);
      if (find_fake(fd) >= 0) {
        if (req == RTC_UIE_ON) {
          uie = 1;
          return 0;
        }
        if (req == RTC_UIE_OFF) {
          uie = 0;
          return 0;
        }
        if (req == RTC_SET_TIME)
          return 0;
        if (req == RTC_RD_TIME) {
          time_t now = time(NULL);
          struct tm tm;
          gmtime_r(&now, &tm);
          memset(arg, 0, sizeof(struct rtc_time));
          memcpy(arg, &tm, sizeof(struct rtc_time));
          return 0;
        }
      }
      if (!real)
        real = dlsym(RTLD_NEXT, "ioctl");
      return real(fd, req, arg);
    }

    __attribute__((constructor)) static void init(void)
    {
      if (getenv("FAKE_RTC_TICK")) {
        pthread_t t;
        pthread_create(&t, NULL, tick, NULL);
      }
    }
    EOF
    $CC -shared -fPIC -O2 -Wall -Werror -o $out/lib/fake-rtc.so shim.c
  '';

  # Waits for READY=1 on a notify socket while chronyd runs; prints the
  # seconds it took or "none" after the limit.
  waitReady = pkgs.writeText "wait-ready.py" ''
    import os, socket, subprocess, sys, time

    chronyd, conf, limit = sys.argv[1], sys.argv[2], float(sys.argv[3])
    path = os.path.join(os.getcwd(), "notify.sock")
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
    sock.bind(path)
    env = dict(os.environ, NOTIFY_SOCKET=path)
    start = time.monotonic()
    proc = subprocess.Popen([chronyd, "-n", "-d", "-x", "-U", "-s", "-f", conf], env=env)
    sock.settimeout(limit)
    try:
        while True:
            if sock.recv(64).startswith(b"READY=1"):
                print(f"{time.monotonic() - start:.1f}")
                break
    except socket.timeout:
        print("none")
    proc.terminate()
    proc.wait()
  '';

  run =
    name: chrony: tick: limit:
    ''
      mkdir -p ${name} && cd ${name}
      cat > chrony.conf <<EOF
      driftfile $PWD/drift
      rtcfile $PWD/rtc
      rtcdevice $PWD/rtc-dev
      port 0
      cmdport 0
      bindcmdaddress /
      pidfile $PWD/chronyd.pid
      EOF
      FAKE_RTC=$PWD/rtc-dev ${if tick then "FAKE_RTC_TICK=1" else ""} \
        LD_PRELOAD=${shim}/lib/fake-rtc.so \
        python3 ${waitReady} ${chrony}/bin/chronyd $PWD/chrony.conf ${toString limit} \
        > ready 2> log
      echo "${name}: READY after $(cat ready) s"
      cd ..
    '';
in
pkgs.runCommand "chrony-rtc-no-interrupts" { nativeBuildInputs = [ pkgs.python3 ]; } ''
  ${run "patched-silent" pkgs.chrony false 40}
  ${run "patched-ticking" pkgs.chrony true 40}
  ${run "stock-silent" stockChrony false 40}

  ready() { cat $1/ready; }

  if [ "$(ready patched-silent)" = none ]; then
    cat patched-silent/log
    echo "patched chronyd never sent READY without RTC interrupts"
    exit 1
  fi
  grep -q "No interrupts from RTC, initial trim skipped" patched-silent/log

  if [ "$(ready patched-ticking)" = none ]; then
    cat patched-ticking/log
    echo "patched chronyd never sent READY with RTC interrupts"
    exit 1
  fi
  if grep -q "initial trim skipped" patched-ticking/log; then
    cat patched-ticking/log
    echo "patched chronyd gave up the initial trim although the RTC interrupted"
    exit 1
  fi
  awk -v t="$(ready patched-ticking)" 'BEGIN { exit !(t < 20) }'

  if [ "$(ready stock-silent)" != none ]; then
    echo "stock chronyd now sends READY without RTC interrupts; drop pkgs/overlays/chrony.nix"
    exit 1
  fi

  touch $out
''
