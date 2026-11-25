"""Time and transport limits shared by the two Excel model paths."""
import asyncio
from contextlib import suppress
import math
import os

import httpx


CONNECT_SECONDS = 10
GATEWAY_MARGIN_SECONDS = 5
REVIEW_RESERVE_SECONDS = 10
MIN_MODEL_SECONDS = 30
_MESSAGES = {
    'MODEL_REASONING_ONLY': '模型把回答额度都用在了思考上，没有生成表格映射，这一部分先保留待确认。',
    'MODEL_TRUNCATED': '模型的回答太长，表格映射没有返回完整。可缩小区块后重试。',
    'MODEL_EMPTY': '模型没有返回表格映射，这一部分可以重试。',
    'MODEL_UPSTREAM_REJECTED': '模型服务拒绝了本次请求。请检查所选模型或请管理员核对模型权限。',
    'MODEL_CONNECTION': '无法连接模型服务，请稍后重试。',
    'MODEL_HTTP_ERROR': '模型服务暂时无法完成核对，请稍后重试。',
    'MODEL_OUTPUT_INVALID': '模型没有返回可用的表格映射，请重试或手动核对。',
    'MODEL_RESPONSE_LIMIT': '模型返回的内容太多，这一部分先保留待确认。',
    'MODEL_CANCELLED': '已取消核对，未确认的部分保留待确认。',
}


class ReviewModelError(ValueError):
    def __init__(self, code, *, timeout=None, retry_thinking_off=False, message=None):
        if code == 'MODEL_TIMEOUT' and timeout is not None:
            message = f'本区块在 {math.ceil(timeout)} 秒内没收到模型结果。已完成的部分可以查看，这一部分可以重试。'
        super().__init__(message or _MESSAGES.get(code, _MESSAGES['MODEL_HTTP_ERROR']))
        self.public_code = code
        self.retry_thinking_off = retry_thinking_off


def model_timeout_seconds():
    try:
        seconds = int(os.environ.get('STRUCTURED_REVIEW_MODEL_TIMEOUT_SECONDS', '180'))
    except ValueError:
        return 180
    return min(300, max(MIN_MODEL_SECONDS, seconds))


def post_review(url, *, headers, timeout, model_timeout, cancelled, max_bytes, trust_env,
                read_error_body=True, **body):
    """Bound the whole HTTP exchange, including slow or partial response bodies."""
    async def post():
        async with httpx.AsyncClient(timeout=httpx.Timeout(timeout, connect=min(CONNECT_SECONDS, timeout)),
                                    trust_env=trust_env, follow_redirects=False) as client:
            async with client.stream('POST', url, headers=headers, **body) as response:
                data = bytearray()
                if response.is_success or read_error_body:
                    async for chunk in response.aiter_bytes():
                        data.extend(chunk)
                        if len(data) > max_bytes:
                            raise ReviewModelError('MODEL_RESPONSE_LIMIT')
                return httpx.Response(response.status_code, content=bytes(data), request=response.request)

    async def run():
        if cancelled and cancelled():
            raise ReviewModelError('MODEL_CANCELLED')
        task = asyncio.create_task(post())
        loop = asyncio.get_running_loop()
        end = loop.time() + timeout
        try:
            while True:
                remaining = end - loop.time()
                if remaining <= 0:
                    raise TimeoutError
                done, _ = await asyncio.wait((task,), timeout=min(remaining, 0.25) if cancelled else remaining)
                if cancelled and cancelled():
                    raise ReviewModelError('MODEL_CANCELLED')
                if loop.time() >= end:
                    raise TimeoutError
                if task in done:
                    return task.result()
        finally:
            if not task.done():
                task.cancel()
                with suppress(asyncio.CancelledError):
                    await task
            elif not task.cancelled():
                task.exception()

    try:
        return asyncio.run(run())
    except httpx.ConnectTimeout:
        raise ReviewModelError('MODEL_CONNECTION') from None
    except (httpx.TimeoutException, TimeoutError):
        raise ReviewModelError('MODEL_TIMEOUT', timeout=model_timeout) from None
    except httpx.HTTPError:
        raise ReviewModelError('MODEL_CONNECTION') from None
