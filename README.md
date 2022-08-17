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

export function teardown() {
  client.close()
}

export default function () {
    client.runTask({
        name: "tasks.add",
        args: [2, 3]
})  // blocks until task is done
    client.runTask({
        name: "tasks.parent",
        args: []
    })   // blocks until all child tasks are done
}
```

Result output:

```

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

     █ teardown

     celery_task_queue_time...: avg=2.48ms  min=1.15ms   med=3.02ms   max=3.27ms  p(90)=3.22ms  p(95)=3.24ms 
     celery_task_runtime......: avg=5.09ms  min=574.35µs med=813.72µs max=13.89ms p(90)=11.27ms p(95)=12.58ms
     celery_tasks.............: 2       74.081224/s
     celery_tasks_child.......: 1       37.040612/s
     celery_tasks_succeeded...: 100.00% ✓ 3          ✗ 0
     celery_tasks_total.......: 3       111.121836/s
     data_received............: 0 B     0 B/s
     data_sent................: 0 B     0 B/s
     iteration_duration.......: avg=12.44ms min=3.44ms   med=12.44ms  max=21.44ms p(90)=19.64ms p(95)=20.54ms
     iterations...............: 1       37.040612/s

```
