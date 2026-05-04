# C3 step 4 — 5-query episodic-memory ablation

Per-instance comparison: control (LOCAGENT_EPISODIC_MEMORY unset) vs treatment (LOCAGENT_EPISODIC_MEMORY=1).

Confirms the prompt change is doing something measurable before paying for C5's full n=200 re-run.

| Instance | Config | File | Class | Func | Episodic? | Stop | Turns | Input tok | Output tok |
|---|---|---|---|---|---|---|---|---|---|
| pandas-dev__pandas-22762 | control | N | N | N | N | consistency | 17 | 192463 | 3883 |
| pandas-dev__pandas-22762 | treatment | N | N | N | Y | consistency | 28 | 554570 | 6499 |
| python__mypy-18163 | control | N | N | Y | N | consistency | 16 | 135549 | 3548 |
| python__mypy-18163 | treatment | N | N | Y | Y | consistency | 18 | 163637 | 4343 |
| vllm-project__vllm-10903 | control | N | N | Y | N | consistency | 35 | 862723 | 5413 |
| vllm-project__vllm-10903 | treatment | N | N | Y | Y | consistency | 33 | 867010 | 7107 |
| PrefectHQ__prefect-16117 | control | N | N | N | N | no_finalize | 40 | 502751 | 4999 |
| PrefectHQ__prefect-16117 | treatment | N | N | Y | Y | consistency | 39 | 736046 | 5125 |
| scipy__scipy-22106 | control | N | N | N | N | consistency | 29 | 408798 | 4471 |
| scipy__scipy-22106 | treatment | N | N | N | Y | consistency | 24 | 270972 | 3999 |

## Top-3 entities per run

**pandas-dev__pandas-22762 / control**: ['pandas.core.arrays.base.ExtensionArray', 'pandas.core.groupby.generic.SeriesGroupBy', 'pandas.core.arrays.integer.IntegerArray']

**pandas-dev__pandas-22762 / treatment**: ['pandas.core.arrays.base.ExtensionArray', 'pandas.core.arrays.integer.IntegerArray', 'pandas.core.groupby.generic.SeriesGroupBy']

**python__mypy-18163 / control**: ['mypy.checker.TypeChecker.equality_type_narrowing_helper', 'mypy.checker.TypeChecker.refine_away_none_in_comparison', 'mypy.checker.TypeChecker.comparison_type_narrowing_helper']

**python__mypy-18163 / treatment**: ['mypy.checker.TypeChecker.equality_type_narrowing_helper', 'mypy.checker.TypeChecker.comparison_type_narrowing_helper', 'mypy.checkexpr.ExpressionChecker.visit_comparison_expr']

**vllm-project__vllm-10903 / control**: ['vllm.v1.worker.gpu_model_runner.GPUModelRunner', 'vllm.v1.worker.gpu_model_runner.GPUModelRunner.execute_model', 'vllm.v1.worker.gpu_model_runner.GPUModelRunner._prepare_inputs']

**vllm-project__vllm-10903 / treatment**: ['vllm.v1.worker.gpu_model_runner.GPUModelRunner', 'vllm.model_executor.models.llava_next.LlavaNextForConditionalGeneration', 'vllm.model_executor.models.llava_onevision.LlavaOnevisionForConditionalGeneration.get_multimodal_embeddings']

**PrefectHQ__prefect-16117 / control**: []

**PrefectHQ__prefect-16117 / treatment**: ['src.prefect.flows.Flow.validate_parameters', 'src.prefect._internal.schemas.validators.validate_parameters_conform_to_schema']

**scipy__scipy-22106 / control**: ['scipy.sparse._construct.eye_array', 'scipy.sparse._construct._eye', 'scipy.sparse._construct.eye']

**scipy__scipy-22106 / treatment**: ['scipy.sparse._construct.eye_array', 'scipy.sparse._construct._eye', 'scipy.sparse._construct.eye']


## Aggregate hit rates

- **control** (n=5): file=0% class=0% func=40%
- **treatment** (n=5): file=0% class=0% func=60%
