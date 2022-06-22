# xk6-celery

This is a [k6](https://go.k6.io/k6) extension using the [xk6](https://github.com/grafana/xk6) system to 
generate load on [celery](https://github.com/celery/celery) worker.

## Build

To build a `k6` binary with this extension, first ensure you have the prerequisites:

- [Go toolchain](https://go101.org/article/go-toolchain.html)
- Git

Then:

1. Install `xk6`:
  ```shell
  go install go.k6.io/xk6/cmd/xk6@latest
  ```

2. Build the binary:
  ```shell
  xk6 build --with github.com/fornfrey/xk6-celery@latest
  ```

## Example

### Celery App

:exclamation: For extension to work ensure [--task-events](https://docs.celeryq.dev/en/stable/reference/cli.html#cmdoption-celery-worker-E) flag is enabled.

Run 
```shell
celery -A tasks worker --task-events
```

tasks.py
```python
from celery import Celery

app = Celery('tasks', broker='amqp://localhost:5672')
app.conf.update(task_send_sent_event=True)  # to collect child tasks metrics ensure this flag is enabled

@app.task
def add(x, y):
    print(x + y)

@app.task
def parent():
    print("parent")
    child.apply_async()

@app.task
def child():
    print("child")
```

### k6

Run 
```shell
k6 run -u 1 -i 1 script.js
```

script.js
```javascript
import celery from 'k6/x/celery';

const client = celery.connect("amqp://localhost:5672", "celery");

export default function () {
    client.runTask("tasks.add", [2, 3])  // blocks until task is done
    client.runTask("tasks.parent", [])   // blocks until all child tasks are done
}
```

Result output:

```
$ ./k6 run script.js

          /\      |‾‾| /‾‾/   /‾‾/   
     /\  /  \     |  |/  /   /  /    
    /  \/    \    |     (   /   ‾‾\  
   /          \   |  |\  \ |  (‾)  | 
  / __________ \  |__| \__\ \_____/ .io

  execution: local
     script: script.js
     output: -

  scenarios: (100.00%) 1 scenario, 1 max VUs, 10m30s max duration (incl. graceful stop):
           * default: 1 iterations shared among 1 VUs (maxDuration: 10m0s, gracefulStop: 30s)


running (00m00.0s), 0/1 VUs, 1 complete and 0 interrupted iterations
default ✓ [======================================] 1 VUs  00m00.0s/10m0s  1/1 shared iters

     celery_task_queue_time...: avg=1.31ms  min=832.31µs med=1.38ms   max=1.71ms  p(90)=1.64ms  p(95)=1.68ms 
     celery_task_runtime......: avg=3.05ms  min=289.44µs med=408.17µs max=8.47ms  p(90)=6.85ms  p(95)=7.66ms 
     celery_tasks.............: 3       218.866576/s
     celery_tasks_child.......: 1       72.955525/s
     celery_tasks_succeeded...: 100.00% ✓ 3          ✗ 0
     data_received............: 0 B     0 B/s
     data_sent................: 0 B     0 B/s
     iteration_duration.......: avg=12.94ms min=12.94ms  med=12.94ms  max=12.94ms p(90)=12.94ms p(95)=12.94ms
     iterations...............: 1       72.955525/s
```
