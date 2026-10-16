from itertools import combinations
from collections import Counter
RANKS=list(range(2,15)); SUITS=[0,1,2,3]
DECK=[(r,s) for r in RANKS for s in SUITS]
def straight_high(rs):
    u=sorted(set(rs))
    if len(u)!=5: return None
    if u[4]-u[0]==4: return u[4]
    if u==[2,3,4,5,14]: return 5
    return None
def classify(h):
    rs=[c[0] for c in h]; ss=[c[1] for c in h]; k=len(h)
    cnt=Counter(rs); shape=sorted(cnt.values(),reverse=True)
    if k==5:
        flush=len(set(ss))==1; sh=straight_high(rs)
        if flush and sh==14: return ("ROYAL",ss[0])
        if flush and sh: return ("SFLUSH",sh,ss[0])
        if shape==[4,1]: return ("QUADS",cnt.most_common(1)[0][0])
        if shape==[3,2]:
            t=[r for r,c in cnt.items() if c==3][0]; p=[r for r,c in cnt.items() if c==2][0]
            return ("FULL",t,p)
        if flush: return ("FLUSH",max(rs),ss[0])
        if sh: return ("STRAIGHT",sh)
    # generalized (k<5 and the k==5 fallthrough shapes)
    if shape[0]==4: return ("QUADS",[r for r,c in cnt.items() if c==4][0])
    if shape[0]==3: return ("TRIPS",[r for r,c in cnt.items() if c==3][0])
    if shape.count(2)==2:
        ps=sorted([r for r,c in cnt.items() if c==2],reverse=True); return ("TWOPAIR",ps[0],ps[1])
    if shape[0]==2: return ("PAIR",[r for r,c in cnt.items() if c==2][0])
    return ("HIGH",max(rs))

params={}
for k in (2,3,4,5):
    for h in combinations(DECK,k):
        c=classify(h); params.setdefault(c[0],set()).add(c[1:])
    print(f"k={k} done")
print()
for cat in ("HIGH","PAIR","TWOPAIR","TRIPS","STRAIGHT","FLUSH","FULL","QUADS","SFLUSH","ROYAL"):
    ps=params.get(cat,set())
    if cat in ("HIGH",):   print(f"  {cat:9s} highs: {sorted({p[0] for p in ps})}  count={len(ps)}")
    elif cat in ("PAIR","TRIPS","QUADS"): print(f"  {cat:9s} ranks: {sorted({p[0] for p in ps})}  count={len(ps)}")
    else: print(f"  {cat:9s} count={len(ps)}")
print("\n  GLOBAL CLAIM SPACE =", sum(len(v) for v in params.values()))
