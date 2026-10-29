import os,re,sys
root=sys.argv[1]; bad=[]; n=0
link=re.compile(r'\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)')
for dp,dn,fn in os.walk(root):
    if '.git' in dp: continue
    for f in fn:
        if not f.endswith('.md'): continue
        p=os.path.join(dp,f); txt=open(p,encoding='utf-8').read()
        txt=re.sub(r'```.*?```','',txt,flags=re.S)
        for m in link.finditer(txt):
            u=m.group(1)
            if re.match(r'^[a-z]+:',u) or u.startswith('#'): continue
            n+=1
            t=os.path.normpath(os.path.join(dp,u.split('#')[0]))
            if not os.path.exists(t): bad.append((os.path.relpath(p,root),u))
print("relative links:",n,"broken:",len(bad))
for b in bad[:30]: print(" ",b)
