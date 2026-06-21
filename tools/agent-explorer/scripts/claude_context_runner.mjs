const originalConsoleError = console.error.bind(console);
console.log = (...args) => process.stderr.write('[LOG] ' + args.join(' ') + '\n');
console.warn = (...args) => process.stderr.write('[WARN] ' + args.join(' ') + '\n');
console.error = (...args) => process.stderr.write('[ERR] ' + args.join(' ') + '\n');

import { createMcpConfig } from '/www/server/nodejs/v24.16.0/lib/node_modules/@zilliz/claude-context-mcp/dist/config.js';
import { createEmbeddingInstance } from '/www/server/nodejs/v24.16.0/lib/node_modules/@zilliz/claude-context-mcp/dist/embedding.js';
import { SnapshotManager } from '/www/server/nodejs/v24.16.0/lib/node_modules/@zilliz/claude-context-mcp/dist/snapshot.js';
import { ToolHandlers } from '/www/server/nodejs/v24.16.0/lib/node_modules/@zilliz/claude-context-mcp/dist/handlers.js';
import { Context, MilvusVectorDatabase } from '/www/server/nodejs/v24.16.0/lib/node_modules/@zilliz/claude-context-mcp/node_modules/@zilliz/claude-context-core/dist/index.js';

async function main() {
  const [op, repo, query = '', limitRaw = '10', forceRaw = 'false'] = process.argv.slice(2);
  const limit = Number.parseInt(limitRaw, 10) || 10;
  const force = forceRaw === 'true';
  if (!op || !repo) {
    throw new Error('usage: claude_context_runner.mjs <search|index|status> <repo> [query] [limit] [force]');
  }

  const config = createMcpConfig();
  const embedding = createEmbeddingInstance(config);
  const vectorDatabase = new MilvusVectorDatabase({
    address: config.milvusAddress,
    ...(config.milvusToken ? { token: config.milvusToken } : {}),
  });
  const context = new Context({
    embedding,
    vectorDatabase,
    collectionNameOverride: config.collectionNameOverride,
  });
  const snapshotManager = new SnapshotManager();
  snapshotManager.loadCodebaseSnapshot();
  const handlers = new ToolHandlers(context, snapshotManager);

  let result;
  if (op === 'search') {
    result = await handlers.handleSearchCode({ path: repo, query, limit });
  } else if (op === 'index') {
    result = await handlers.handleIndexCodebase({ path: repo, force });
  } else if (op === 'index-wait') {
    result = await handlers.handleIndexCodebase({ path: repo, force });
    const startedText = result?.content?.[0]?.text || '';
    if (!/Started background indexing/i.test(startedText)) {
      process.stdout.write(JSON.stringify(result));
      return;
    }
    const startedAt = Date.now();
    const timeoutMs = 30 * 60 * 1000;
    while (Date.now() - startedAt < timeoutMs) {
      await new Promise((r) => setTimeout(r, 5000));
      const status = await handlers.handleGetIndexingStatus({ path: repo });
      const text = status?.content?.[0]?.text || '';
      if (!/indexing in progress/i.test(text)) {
        process.stdout.write(JSON.stringify(status));
        return;
      }
    }
    process.stdout.write(JSON.stringify({
      content: [{ type: 'text', text: `Indexing still running for '${repo}' after timeout.` }],
      isError: true,
    }));
    return;
  } else if (op === 'status') {
    result = await handlers.handleGetIndexingStatus({ path: repo });
  } else {
    throw new Error(`unknown op: ${op}`);
  }

  process.stdout.write(JSON.stringify(result));
}

main().catch((err) => {
  originalConsoleError(JSON.stringify({
    isError: true,
    content: [{ type: 'text', text: String(err && err.stack ? err.stack : err) }],
  }));
  process.exit(1);
});
