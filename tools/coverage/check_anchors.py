import os,re,sys
root=sys.argv[1]
def slug(h):
    t=h.strip().lower(); t=re.sub(r'[^\w\- ]','',t); return t.replace(' ','-')
heads={}
for dp,dn,fn in os.walk(root):
    dn[:]=[d for d in dn if d!='.git']
    for f in fn:
        if f.endswith('.md'):
            p=os.path.normpath(os.path.join(dp,f)); s=open(p,encoding='utf-8').read()
            s2=re.sub(r'```.*?```','',s,flags=re.S)
            heads[p]={slug(m.group(1)) for m in re.finditer(r'^#{1,6} (.+)$',s2,re.M)}
            heads[p]|={m.group(1) for m in re.finditer(r'<a (?:id|name)="([^"]+)"',s2)}
bad=[];n=0
link=re.compile(r'\]\(([^)\s#]*)#([^)\s]+)\)')
for p in heads:
    s=re.sub(r'```.*?```','',open(p,encoding='utf-8').read(),flags=re.S)
    for m in link.finditer(s):
        f,a=m.group(1),m.group(2)
        if re.match(r'^[a-z]+:',f): continue
        t=os.path.normpath(os.path.join(os.path.dirname(p),f)) if f else p
        if t not in heads: continue
        n+=1
        if a not in heads[t]: bad.append((os.path.relpath(p,root),f+'#'+a))
print("anchor links:",n,"broken:",len(bad))
import collections
for (src,l),c in collections.Counter(bad).most_common(25): print(" ",c,src,l)
