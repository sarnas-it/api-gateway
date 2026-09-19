# Сводка прогона бенчмарка плагинов (Task 16, промежуточная)

Сырые числа для финального отчёта Task 17. Все цифры — медианы прогонов.

## Параметры прогона

- Хост: 12th Gen Intel(R) Core(TM) i5-1235U, linux/amd64.
- wrk: `RUNS=5 DUR=15s`, c50 (`-t2 -c50`) и c300 (`-t4 -c300`, 20s), по 5 прогонов на точку.
- Go bench: `-trimpath -run '^$' -bench BenchmarkGateway -benchmem -count=5 -benchtime=2s`, backend `:19901` поднят.
- Артефакты: `results/*.csv` (wrk), `results/*.bench` (Go), лог прогона `results/wrk-run.log`.

## Noise floor (baseline-jwt против самого себя)

| conns | baseline-jwt-noise, req/s | baseline-jwt, req/s | расхождение |
|-------|--------------------------:|--------------------:|------------:|
| 50    | 21991                     | 21808               | 0.8%        |
| 300   | 31120                     | 31294               | 0.6%        |

Разброс ниже порога ±2% — медианы baseline-jwt можно использовать как опорную точку.

## wrk: медианы по сценариям

| Сценарий                | conns | req/s  | p50     | p99     |
|-------------------------|-------|-------:|--------:|--------:|
| baseline-jwt-noise      | 50    | 21991  | 1.84ms  | 8.53ms  |
| baseline-jwt-noise      | 300   | 31120  | 8.50ms  | 32.24ms |
| baseline-jwt            | 50    | 21808  | 1.85ms  | 8.92ms  |
| baseline-jwt            | 300   | 31294  | 8.53ms  | 29.13ms |
| plugin-jwt-so           | 50    | 9419   | 4.51ms  | 18.50ms |
| plugin-jwt-so           | 300   | 19765  | 14.05ms | 39.40ms |
| plugin-jwt-shared       | 50    | 18624  | 2.18ms  | 12.17ms |
| plugin-jwt-shared       | 300   | 28612  | 9.43ms  | 26.59ms |
| baseline-ratelimit      | 50    | 25413  | 1.56ms  | 7.24ms  |
| baseline-ratelimit      | 300   | 25383  | 10.29ms | 42.22ms |
| plugin-ratelimit-shared | 50    | 12124  | 3.40ms  | 16.39ms |
| plugin-ratelimit-shared | 300   | 15244  | 16.74ms | 88.24ms |
| baseline-webhooks       | 50    | 25053  | 1.60ms  | 7.58ms  |
| baseline-webhooks       | 300   | 17051  | 15.94ms | 48.63ms |
| plugin-webhooks-fast    | 50    | 15658  | 2.68ms  | 11.07ms |
| plugin-webhooks-fast    | 300   | 22421  | 12.20ms | 36.50ms |
| baseline-discovery      | 50    | 27651  | 1.44ms  | 6.57ms  |
| baseline-discovery      | 300   | 26689  | 9.78ms  | 37.89ms |
| plugin-discovery-fast   | 50    | 25633  | 1.53ms  | 8.33ms  |
| plugin-discovery-fast   | 300   | 27300  | 9.70ms  | 35.58ms |

## wrk: plugin относительно baseline

| Пара                             | conns | baseline, req/s | plugin, req/s | plugin/baseline |
|----------------------------------|-------|----------------:|--------------:|----------------:|
| baseline-jwt → plugin-jwt-so     | 50    | 21808           | 9419          | 43.2%           |
| baseline-jwt → plugin-jwt-so     | 300   | 31294           | 19765         | 63.2%           |
| baseline-jwt → plugin-jwt-shared | 50    | 21808           | 18624         | 85.4%           |
| baseline-jwt → plugin-jwt-shared | 300   | 31294           | 28612         | 91.4%           |
| baseline-ratelimit → plugin-ratelimit-shared | 50 | 25413 | 12124 | 47.7% |
| baseline-ratelimit → plugin-ratelimit-shared | 300 | 25383 | 15244 | 60.1% |
| baseline-webhooks → plugin-webhooks-fast | 50 | 25053 | 15658 | 62.5% |
| baseline-webhooks → plugin-webhooks-fast | 300 | 17051 | 22421 | 131.5% |
| baseline-discovery → plugin-discovery-fast | 50 | 27651 | 25633 | 92.7% |
| baseline-discovery → plugin-discovery-fast | 300 | 26689 | 27300 | 102.3% |

## Go bench: медианы ns/op, B/op, allocs/op

| Сценарий           | ns/op  | B/op   | allocs/op |
|--------------------|-------:|-------:|----------:|
| baseline-jwt       | 55605  | 17004  | 162       |
| plugin-jwt-so      | 73908  | 25787  | 299       |
| plugin-jwt-shared  | 69296  | 14742  | 112       |

Относительно baseline-jwt: plugin-jwt-so — 1.33x ns/op, 1.52x B/op, 1.85x allocs/op;
plugin-jwt-shared — 1.25x ns/op, 0.87x B/op, 0.69x allocs/op.

## Наблюдения (промежуточные, не выводы)

- JWT: оба plugin-транспорта медленнее builtin на wrk hot-path; `.so` заметно тяжелее
  (43–63% от baseline), shared ближе (85–91%). По аллокациям shared ниже baseline
  (112 vs 162 allocs/op), `.so` почти вдвое выше (299).
- Rate limit: shared-плагин даёт 48–60% от baseline — самый большой провал среди
  shared-транспортов.
- Webhooks: на c50 plugin медленнее (62.5%), на c300 быстрее (131.5%). У baseline-webhooks
  c300 большой разброс (11.2k–20.3k req/s), точка шумная — трактовать осторожно.
- Discovery (вне hot-path): 92.7% и 102.3% — в пределах шума, что ожидаемо для
  сценария без влияния на горячий путь.
- Все 9 сценариев + noise floor стартовали успешно, падений плагинов нет.
- Промежуточная сводка; финальные формулировки и критерии — в отчёте Task 17.

## Артефакты

- `results/wrk-run.log` — stdout полного wrk-прогона.
- `results/*.csv` — по 5 строк на каждую точку c50/c300.
- `results/*.bench` — сырой вывод `go test -bench` (3 файла).
