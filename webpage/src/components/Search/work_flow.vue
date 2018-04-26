<template>
  <div>
    <Row>
      <Card>
        <div class="step-header-con">
          <h3>{{ stepData.title }}</h3>
          <h5>{{ stepData.describe }}</h5>
        </div>
        <p class="step-content" v-html="stepData.content"></p>
        <Form class="step-form" ref="step" :model="step" :rules="stepRules" :label-width="150">
          <FormItem label="查询说明：" prop="opinion">
            <Input v-model="step.opinion" type="textarea" :autosize="{minRows: 2,maxRows: 5}" placeholder="请填写查询说明" />
          </FormItem>
          <FormItem label="查询时限：" prop="timer">
            <Input v-model="step.timer"  placeholder="请填写查询时限，单位：分钟 （只填写数字）" style="width: 25%;"/>
          </FormItem>
          <FormItem label="">
            <Button  @click="handleSubmit" style="width:100px;" type="primary">提交</Button>
          </FormItem>
        </Form>
        <Steps>
          <Step v-for="item in stepList1" :title="item.title" :content="item.describe + '审核并通过'" :key="item.title"></Step>
        </Steps>
      </Card>
    </Row>
  </div>
</template>

<script>
  import Cookies from 'js-cookie'
  import axios from 'axios'
  import util from '../../libs/util'
  export default {
    name: 'work_flow',
    props: ['msg'],
    data () {
      return {
        stepData: {
          title: 'Yearning自助SQL查询系统',
          describe: Cookies.get('user'),
          content: '欢迎使用Yearning自助SQL查询系统</br></br>   请在使用中遵守以下注意事项：</br></br>  1.必须填写查询说明</br></br>  2.根据查询条件预估所需的查询时间 </br></br>  3.不可提交慢查询等严重影响性能的查询语句 </br></br> 4.所有提交的查询语句均会进行审计记录'
        },
        step: {
          remark: '',
          timer: ''
        },
        stepList1: [
          {
            title: '填写工单',
            describe: '提交'
          },
          {
            title: '进入查询页面',
            describe: '自助'
          }
        ],
        stepRules: {
          opinion: [
            { required: true, message: '请填写查询说明', trigger: 'blur' }
          ],
          timer: [
            { required: true, message: '请填写查询时限', trigger: 'blur' }
          ]
        }
      }
    },
    methods: {
      handleSubmit () {
        this.$refs['step'].validate((valid) => {
          if (valid) {
            let workid = 1000000000 * 100000000 * Math.random()
            axios.get(`${util.url}/search?timer=${this.step.timer}&mode=put&workid=${workid}&instructions=${this.step.opinion}`)
            this.$emit('render', workid)
          }
        });
      }
    },
    mounted () {
      axios.get(`${util.url}/search?timer=${this.step.timer}&mode=get`)
        .then(res => {
          this.$emit('res', res.data)
        })
    }
  }
</script>

<style lang="less">
  .step{
    &-header-con{
      text-align: center;
      h3{
        margin: 10px 0;
      }
      h5{
        margin: 0 0 5px;
      }
    }
    &-content{
      padding: 5px 20px 26px;
      margin-bottom: 20px;
      border-bottom: 1px solid #dbdddf;
    }
    &-form{
      padding-bottom: 10px;
      border-bottom: 1px solid #dbdddf;
      margin-bottom: 20px;
    }
  }
</style>
