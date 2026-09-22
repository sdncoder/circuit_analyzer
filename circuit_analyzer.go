// circuit_analyzer.go analyzes wide CSV exports of interface HC Octets metrics.
// It groups columns by circuit identifier, reports period-over-period trends,
// detects >=90% traffic drops, and identifies circuits that likely absorbed traffic.
// Standard library only.
package main

import (
    "encoding/csv"
    "flag"
    "fmt"
    "io"
    "math"
    "os"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"
)

type Sample struct { T time.Time; V []float64 }
type Circuit struct { Name string; Cols []int }
type Trend struct { Name string; Base, Current, Pct float64; BaseN, CurrentN int }
type Candidate struct { Name string; Pre, During, Gain, RisePct, Corr, Score float64 }
type Failure struct { When time.Time; Failed string; Baseline, Value, DropPct, Lost float64; Candidates []Candidate }

var (
    cidRE = regexp.MustCompile(`(?i)T:NWI_M:1_(.*?)\s*\|\s*PEER:`)
    cidAltRE = regexp.MustCompile(`(?i)T:NWI_M1_CID:\s*(.*?)\s*\|\s*PEER:`)
    leadCIDRE = regexp.MustCompile(`(?i)^CID:\s*`)
    dotsRE = regexp.MustCompile(`\.+$`)
    spacesRE = regexp.MustCompile(`\s+`)
)

func main() {
    input := flag.String("input", "", "input CSV file")
    outPrefix := flag.String("out", "circuit-analysis", "output file prefix")
    failureDrop := flag.Float64("failure-drop", 90, "failure threshold: percent drop from rolling baseline")
    baselineSamples := flag.Int("baseline-samples", 12, "samples in rolling failure baseline")
    minBaselineSamples := flag.Int("min-baseline-samples", 6, "minimum valid samples for failure baseline")
    takeoverWindow := flag.Int("takeover-window", 2, "current/following samples examined for takeover")
    candidateRise := flag.Float64("candidate-rise", 40, "minimum candidate traffic rise percent")
    minCandidateGain := flag.Float64("min-candidate-gain", 0.5, "minimum absolute candidate gain")
    trendDays := flag.Int("trend-days", 7, "days in early/late trend windows")
    top := flag.Int("top", 5, "maximum takeover candidates per failure")
    flag.Parse()

    if *input == "" { die("-input is required") }
    headers, samples, circuits, err := loadCSV(*input)
    if err != nil { die(err.Error()) }
    _ = headers
    if len(samples) < 2 { die("not enough timestamped rows") }

    trend := analyzeTrend(samples, circuits, *trendDays)
    failures := analyzeFailures(samples, circuits, *failureDrop, *baselineSamples, *minBaselineSamples, *takeoverWindow, *candidateRise, *minCandidateGain, *top)

    trendFile := *outPrefix + "-trend.csv"
    failureFile := *outPrefix + "-failures.csv"
    if err := writeTrend(trendFile, trend); err != nil { die(err.Error()) }
    if err := writeFailures(failureFile, failures); err != nil { die(err.Error()) }
    printSummary(samples, trend, failures, trendFile, failureFile)
}

func loadCSV(path string) ([]string, []Sample, []Circuit, error) {
    f, err := os.Open(path); if err != nil { return nil,nil,nil,err }; defer f.Close()
    r := csv.NewReader(f); r.FieldsPerRecord = -1; r.ReuseRecord = false
    h, err := r.Read(); if err != nil { return nil,nil,nil,err }
    metricCols := []int{}
    groups := map[string][]int{}
    for i := 1; i < len(h); i++ {
        if strings.HasSuffix(strings.TrimSpace(h[i]), "(units)") { continue }
        metricCols = append(metricCols, i)
        k := circuitKey(h[i]); groups[k] = append(groups[k], i)
    }
    names := make([]string,0,len(groups)); for k := range groups { names=append(names,k) }; sort.Strings(names)
    circuits := make([]Circuit,len(names)); for i,n := range names { circuits[i]=Circuit{n,groups[n]} }
    samples := []Sample{}
    for {
        row, e := r.Read(); if e==io.EOF { break }; if e!=nil { return nil,nil,nil,e }; if len(row)==0 { continue }
        t, e := parseTime(row[0]); if e!=nil { continue }
        vals := make([]float64,len(circuits)); for i := range vals { vals[i]=math.NaN() }
        for ci,c := range circuits {
            sum,n := 0.0,0
            for _,col := range c.Cols { if col<len(row) { if v,e:=strconv.ParseFloat(strings.TrimSpace(row[col]),64); e==nil { sum+=v; n++ } } }
            if n>0 { vals[ci]=sum/float64(n) }
        }
        samples=append(samples,Sample{t,vals})
    }
    sort.Slice(samples,func(i,j int)bool{return samples[i].T.Before(samples[j].T)})
    return h,samples,circuits,nil
}

func circuitKey(h string) string {
    var s string
    if m:=cidRE.FindStringSubmatch(h); len(m)>1 { s=m[1] } else if m:=cidAltRE.FindStringSubmatch(h); len(m)>1 { s=m[1] } else { s=h }
    s=leadCIDRE.ReplaceAllString(strings.TrimSpace(s),""); s=dotsRE.ReplaceAllString(s,""); return spacesRE.ReplaceAllString(s," ")
}

func parseTime(s string)(time.Time,error){
    s=strings.TrimSpace(s); if p:=strings.Index(s,","); p>=0 { s=strings.TrimSpace(s[p+1:]) }
    for _,z:=range []string{" UTC-05:00 EST"," UTC-04:00 EDT"}{ s=strings.TrimSuffix(s,z) }
    layouts:=[]string{"January 2 2006 3:04 pm","January 02 2006 3:04 pm",time.RFC3339,"2006-01-02 15:04:05"}
    var last error; for _,l:=range layouts { if t,e:=time.Parse(l,s); e==nil{return t,nil}else{last=e} }; return time.Time{},last
}

func analyzeTrend(s []Sample,c []Circuit,days int)[]Trend{
    start,end:=s[0].T,s[len(s)-1].T; d:=time.Duration(days)*24*time.Hour
    bEnd:=start.Add(d); curStart:=end.Add(-d)
    out:=make([]Trend,0,len(c))
    for j,x:=range c { b,bn:=meanRange(s,j,start,bEnd); v,vn:=meanRange(s,j,curStart,end.Add(time.Nanosecond)); p:=math.NaN(); if bn>0&&vn>0&&b!=0{p=(v/b-1)*100}; out=append(out,Trend{x.Name,b,v,p,bn,vn}) }
    sort.Slice(out,func(i,j int)bool{return finite(out[i].Pct)&&(!finite(out[j].Pct)||out[i].Pct>out[j].Pct)})
    return out
}
func meanRange(s []Sample,j int,a,b time.Time)(float64,int){sum:=0.0;n:=0;for _,x:=range s{if !x.T.Before(a)&&x.T.Before(b)&&finite(x.V[j]){sum+=x.V[j];n++}};if n==0{return math.NaN(),0};return sum/float64(n),n}

func analyzeFailures(s []Sample,c []Circuit,drop float64,baseN,minN,window int,minRise,minGain float64,top int)[]Failure{
    out:=[]Failure{}; last:=map[int]time.Time{}
    for i:=baseN;i<len(s);i++ { for j,x:=range c {
        hist:=[]float64{}; for k:=max(0,i-baseN);k<i;k++{if finite(s[k].V[j]){hist=append(hist,s[k].V[j])}}
        if len(hist)<minN {continue}; baseline:=median(hist); v:=s[i].V[j]; if !finite(v)||baseline<=0.1{continue}
        dp:=(1-v/baseline)*100; if dp<drop{continue}
        if t,ok:=last[j];ok&&s[i].T.Sub(t)<=12*time.Hour{continue};last[j]=s[i].T
        preFailed:=medianColumn(s,j,max(0,i-6),i); duringFailed:=maxColumn(s,j,i,min(len(s),i+max(1,window))); lost:=preFailed-duringFailed
        cand:=[]Candidate{}
        for g,y:=range c {if g==j{continue};pre:=medianColumn(s,g,max(0,i-6),i);during:=maxColumn(s,g,i,min(len(s),i+max(1,window)));if !finite(pre)||!finite(during)||pre<=0.1{continue};gain:=during-pre;rise:=gain/pre*100;if gain<minGain||rise<minRise{continue};corr:=pearson(s,j,g);score:=gain/math.Max(lost,0.01)+math.Max(-corr,0);cand=append(cand,Candidate{y.Name,pre,during,gain,rise,corr,score})}
        sort.Slice(cand,func(a,b int)bool{return cand[a].Score>cand[b].Score});if len(cand)>top{cand=cand[:top]}
        out=append(out,Failure{s[i].T,x.Name,baseline,v,dp,lost,cand})
    }}
    sort.Slice(out,func(i,j int)bool{return out[i].When.Before(out[j].When)});return out
}

func medianColumn(s []Sample,j,a,b int)float64{v:=[]float64{};for i:=a;i<b;i++{if finite(s[i].V[j]){v=append(v,s[i].V[j])}};return median(v)}
func maxColumn(s []Sample,j,a,b int)float64{m:=math.NaN();for i:=a;i<b;i++{v:=s[i].V[j];if finite(v)&&(!finite(m)||v>m){m=v}};return m}
func median(v []float64)float64{if len(v)==0{return math.NaN()};sort.Float64s(v);n:=len(v);if n%2==1{return v[n/2]};return(v[n/2-1]+v[n/2])/2}
func pearson(s []Sample,a,b int)float64{xa,xb:=[]float64{},[]float64{};for _,x:=range s{if finite(x.V[a])&&finite(x.V[b]){xa=append(xa,x.V[a]);xb=append(xb,x.V[b])}};if len(xa)<3{return 0};ma,mb:=avg(xa),avg(xb);num,da,db:=0.0,0.0,0.0;for i:=range xa{x:=xa[i]-ma;y:=xb[i]-mb;num+=x*y;da+=x*x;db+=y*y};if da==0||db==0{return 0};return num/math.Sqrt(da*db)}
func avg(v []float64)float64{s:=0.0;for _,x:=range v{s+=x};return s/float64(len(v))}

func writeTrend(path string,x []Trend)error{f,e:=os.Create(path);if e!=nil{return e};defer f.Close();w:=csv.NewWriter(f);defer w.Flush();w.Write([]string{"circuit","baseline_average","current_average","percent_change","baseline_samples","current_samples"});for _,r:=range x{w.Write([]string{r.Name,num(r.Base),num(r.Current),num(r.Pct),strconv.Itoa(r.BaseN),strconv.Itoa(r.CurrentN)})};return w.Error()}
func writeFailures(path string,x []Failure)error{f,e:=os.Create(path);if e!=nil{return e};defer f.Close();w:=csv.NewWriter(f);defer w.Flush();w.Write([]string{"failure_time","failed_circuit","rolling_baseline","failure_value","drop_percent","estimated_lost_traffic","candidate_rank","takeover_candidate","candidate_pre","candidate_during","candidate_gain","candidate_rise_percent","inverse_correlation","score"});for _,r:=range x{if len(r.Candidates)==0{w.Write([]string{r.When.Format(time.RFC3339),r.Failed,num(r.Baseline),num(r.Value),num(r.DropPct),num(r.Lost),"","","","","","","",""})}else{for i,c:=range r.Candidates{w.Write([]string{r.When.Format(time.RFC3339),r.Failed,num(r.Baseline),num(r.Value),num(r.DropPct),num(r.Lost),strconv.Itoa(i+1),c.Name,num(c.Pre),num(c.During),num(c.Gain),num(c.RisePct),num(c.Corr),num(c.Score)})}}};return w.Error()}
func printSummary(s []Sample,t []Trend,f []Failure,tf,ff string){up,down:=0,0;for _,x:=range t{if finite(x.Pct){if x.Pct>0{up++}else if x.Pct<0{down++}}};fmt.Printf("Data range: %s to %s\nCircuits: %d | Trending up: %d | Trending down: %d\nFailure events: %d\nTrend report: %s\nFailure report: %s\n",s[0].T.Format("2006-01-02 15:04"),s[len(s)-1].T.Format("2006-01-02 15:04"),len(t),up,down,len(f),tf,ff);fmt.Println("\nTop increases:");for i:=0;i<min(10,len(t));i++{if finite(t[i].Pct){fmt.Printf("  %-45s %8.1f%%\n",t[i].Name,t[i].Pct)}};fmt.Println("\nFailure/takeover summary:");for _,x:=range f{take:="no clear takeover";if len(x.Candidates)>0{take=fmt.Sprintf("%s (+%.1f%%)",x.Candidates[0].Name,x.Candidates[0].RisePct)};fmt.Printf("  %s | %s dropped %.1f%% | %s\n",x.When.Format("2006-01-02 15:04"),x.Failed,x.DropPct,take)}}
func num(x float64)string{if !finite(x){return ""};return strconv.FormatFloat(x,'f',6,64)}
func finite(x float64)bool{return !math.IsNaN(x)&&!math.IsInf(x,0)}
func min(a,b int)int{if a<b{return a};return b};func max(a,b int)int{if a>b{return a};return b}
func die(s string){fmt.Fprintln(os.Stderr,"Error:",s);os.Exit(1)}
