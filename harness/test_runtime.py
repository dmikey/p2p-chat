import asyncio
import json
import unittest
from agents import RunConfig
from agents.tool_context import ToolContext
from runtime import calculate, build_agent

class CapabilityBoundary(unittest.IsolatedAsyncioTestCase):
    async def invoke(self, operation, left, right):
        args=json.dumps(dict(operation=operation,left=left,right=right))
        ctx=ToolContext(context=None,tool_name='calculate',tool_call_id='test',tool_arguments=args,
                        run_config=RunConfig(tracing_disabled=True,trace_include_sensitive_data=False))
        return await calculate.on_invoke_tool(ctx,args)
    async def test_sdk_tool_can_calculate_but_cannot_execute_code(self):
        self.assertEqual(await self.invoke('multiply',18,23),'414.0')
        self.assertIn('Unsupported',await self.invoke('__import__("os").system',1,1))
        self.assertIn('Unsupported',await self.invoke('divide',1,0))
        self.assertIn('finite',await self.invoke('multiply',1e20,2))
    async def test_only_declared_capability_is_exposed(self):
        self.assertEqual([t.name for t in build_agent('numbers','test-model').tools],['calculate'])
        self.assertEqual(build_agent('plan','test-model').tools,[])
        self.assertFalse(build_agent('write','test-model').model_settings.store)

if __name__=='__main__': unittest.main()
