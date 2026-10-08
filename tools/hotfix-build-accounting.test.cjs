'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),cp=require('node:child_process');
const {account,changeBytes}=require('./hotfix-build-accounting.cjs');
const root=path.resolve(__dirname,'..');
const git=(...args)=>cp.execFileSync('git',['-C',root,...args],{encoding:'utf8'}).trim();
const history=[['914c6c5','ed9faca'],['3c88289','d5f9491'],['4015f97','dd82ca5'],['0e8ab48','5ae2039'],['f90f995','28e3b47']].map(([commit,original])=>({commit:git('rev-parse',commit),original:git('rev-parse',original)}));
const fixes=['4a89334','a256c37','9fdb0e7','8be2e3d','e875a6a','73c7f05','d614c0d','7ba87b3','7b643f4'];
test('retained ports are proved already in the field baseline',()=>assert.deepEqual(account(root,'c05f8b2','7b643f4',fixes,history).undeclared,[]));
test('missing retained-history proof remains undeclared',()=>assert.equal(account(root,'c05f8b2','7b643f4',fixes).undeclared.length,5));
test('a different baseline runtime patch is rejected',()=>assert.throws(()=>account(root,'c05f8b2','7b643f4',fixes,[{...history[0],original:history[1].original}]),/patch differs/));
test('a post-baseline change cannot be called retained history',()=>assert.throws(()=>account(root,'c05f8b2','7b643f4',fixes,[{commit:git('rev-parse','7b643f4'),original:history[0].original}]),/ancestry/));
test('an original outside the field baseline is rejected',()=>assert.throws(()=>account(root,'c05f8b2','7b643f4',fixes,[{...history[0],original:git('rev-parse','7b643f4')}]),/ancestry/));
test('duplicate or unknown history fields are rejected',()=>{
 assert.throws(()=>account(root,'c05f8b2','7b643f4',fixes,[history[0],history[0]]),/ancestry/);
 assert.throws(()=>account(root,'c05f8b2','7b643f4',fixes,[{...history[0],skip:true}]),/fields/);
});
test('history proof cannot hide a new undeclared fix',()=>assert.ok(account(root,'c05f8b2','7b643f4',fixes.slice(0,-1),history).undeclared.includes(git('rev-parse','7b643f4'))));
test('runtime string whitespace is not equivalent while context offsets are irrelevant',()=>{
 const diff=text=>'diff --git a/internal/x.go b/internal/x.go\n--- a/internal/x.go\n+++ b/internal/x.go\n@@ -1 +1 @@\n-old\n+return "'+text+'"\n';
 assert.notEqual(changeBytes(diff('denied request')),changeBytes(diff('deniedrequest')));
 assert.equal(changeBytes(diff('denied request')),changeBytes(diff('denied request').replace('@@ -1 +1 @@','@@ -50 +100 @@')));
});
